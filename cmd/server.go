// Package cmd implements the command-line interface for the fly-user-env service.
// The service manages long-running processes and provides HTTP proxying capabilities.
package cmd

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"fly-user-env/lib"
)

// ServerCleanup represents a cleanup operation that can be deferred
type ServerCleanup struct {
	mu     sync.Mutex
	tasks  []func() error
	done   bool
	errors []error
}

// Add adds a cleanup task to be executed
func (c *ServerCleanup) Add(task func() error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.done {
		c.tasks = append(c.tasks, task)
	}
}

// Execute runs all cleanup tasks in reverse order
func (c *ServerCleanup) Execute() {
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
func (c *ServerCleanup) Errors() []error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.errors
}

// RunServer starts the server with the following responsibilities:
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
// Optional environment variables for configuration:
//   - FLY_STORAGE_BUCKET: S3 bucket name
//   - FLY_STORAGE_ENDPOINT: S3 endpoint URL
//   - FLY_STORAGE_ACCESS_KEY: S3 access key
//   - FLY_STORAGE_SECRET_KEY: S3 secret key
//   - FLY_STORAGE_REGION: S3 region (optional)
//   - FLY_STACKS: Comma-separated list of stack components to enable
//   - FLY_ENV_WAIT_FOR_CONFIG: If set, wait for config via HTTP endpoint
//
// Returns an error if the service fails to start, and a cleanup function that should be called on shutdown.
func RunServer() (error, *ServerCleanup, *lib.Supervisor) {
	cleanup := &ServerCleanup{}

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
	control := lib.NewControl(*targetAddr, "fly-app-controller", token, "tmp", supervisor)

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

// RunServerAndWait starts the server and waits for shutdown signals.
// This is the main entry point for the server command.
func RunServerAndWait() error {
	err, cleanup, supervisor := RunServer()
	if err != nil {
		return err
	}

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	for {
		sig := <-sigChan
		log.Printf("Received signal: %v, forwarding to supervised process", sig)
		if supervisor != nil {
			supervisor.ForwardSignal(sig)
		}
		if sig == syscall.SIGINT || sig == syscall.SIGTERM {
			break
		}
	}

	log.Printf("Shutting down...")
	cleanup.Execute()
	if errs := cleanup.Errors(); len(errs) > 0 {
		log.Printf("Cleanup completed with %d errors", len(errs))
		return fmt.Errorf("cleanup completed with %d errors", len(errs))
	}
	log.Printf("Cleanup completed successfully")
	return nil
}
