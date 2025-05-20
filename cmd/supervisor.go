// Package cmd implements the command-line interface for the supervisor service.
// The supervisor manages long-running processes and provides HTTP proxying capabilities.
package cmd

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"supervisor/lib"
)

// Cleanup represents a cleanup operation that can be deferred
type Cleanup struct {
	mu     sync.Mutex
	tasks  []func() error
	done   bool
	errors []error
}

// Add adds a cleanup task to be executed
func (c *Cleanup) Add(task func() error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.done {
		c.tasks = append(c.tasks, task)
	}
}

// Execute runs all cleanup tasks in reverse order
func (c *Cleanup) Execute() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return
	}
	c.done = true

	// Execute tasks in reverse order (LIFO)
	for i := len(c.tasks) - 1; i >= 0; i-- {
		if err := c.tasks[i](); err != nil {
			c.errors = append(c.errors, err)
			log.Printf("Cleanup task failed: %v", err)
		}
	}
}

// Errors returns any errors that occurred during cleanup
func (c *Cleanup) Errors() []error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.errors
}

// RunSupervisor starts the supervisor service with the following responsibilities:
// - Manages a long-running process specified by command-line arguments
// - Provides an admin interface for configuration and status
// - Proxies HTTP requests to the supervised process
// - Routes requests based on the Host header
//
// Required flags:
//   - --listen: Address to listen on (default: 0.0.0.0:8080)
//   - --target: Address to proxy to (required)
//
// Required environment variables:
//   - CONTROLLER_TOKEN: Token for admin interface access
//
// Returns an error if the service fails to start, and a cleanup function that should be called on shutdown.
func RunSupervisor() (error, *Cleanup, *lib.Supervisor) {
	cleanup := &Cleanup{}

	listenAddr := flag.String("listen", "0.0.0.0:8080", "Address to listen on")
	targetAddr := flag.String("target", "", "Address to proxy to")
	flag.Parse()

	if *targetAddr == "" {
		return fmt.Errorf("--target flag is required"), cleanup, nil
	}

	args := flag.Args()
	if len(args) == 0 {
		return fmt.Errorf("command to supervise is required"), cleanup, nil
	}

	token := os.Getenv("CONTROLLER_TOKEN")
	if token == "" {
		return fmt.Errorf("CONTROLLER_TOKEN environment variable is required for controller access"), cleanup, nil
	}

	// Get default config
	config := lib.DefaultAdminConfig()

	supervisor := lib.NewSupervisor(args, lib.SupervisorConfig{
		TimeoutStop:  config.TimeoutStop,
		RestartDelay: config.RestartDelay,
	})
	// Create control instance
	control := lib.NewControl("localhost:8080", "test-token", "tmp", supervisor)

	proxy, err := lib.New(*targetAddr, supervisor)
	if err != nil {
		return fmt.Errorf("failed to create proxy: %v", err), cleanup, nil
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if strings.EqualFold(host, "fly-app-controller") {
			log.Printf("[supervisor] Routing to admin interface for host: %s", host)
			control.ServeHTTP(w, r)
			return
		}
		log.Printf("[supervisor] Routing to proxy for host: %s", host)
		proxy.ServeHTTP(w, r)
	})

	mux := http.NewServeMux()
	mux.Handle("/", handler)

	server := &http.Server{
		Addr:    *listenAddr,
		Handler: mux,
	}

	// Add server shutdown to cleanup
	cleanup.Add(func() error {
		return server.Close()
	})

	log.Printf("Starting supervisor on %s, proxying to %s", *listenAddr, *targetAddr)

	// Start server in a goroutine
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	return nil, cleanup, supervisor
}
