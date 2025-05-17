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

	"supervisor/lib"
)

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
// Returns an error if the service fails to start.
func RunSupervisor() error {
	listenAddr := flag.String("listen", "0.0.0.0:8080", "Address to listen on")
	targetAddr := flag.String("target", "", "Address to proxy to")
	flag.Parse()

	if *targetAddr == "" {
		return fmt.Errorf("--target flag is required")
	}

	args := flag.Args()
	if len(args) == 0 {
		return fmt.Errorf("command to supervise is required")
	}

	token := os.Getenv("CONTROLLER_TOKEN")
	if token == "" {
		return fmt.Errorf("CONTROLLER_TOKEN environment variable is required for controller access")
	}

	supervisor := lib.NewSupervisor(args)
	admin := lib.NewAdmin(supervisor, token)

	proxy, err := lib.New(*targetAddr, supervisor)
	if err != nil {
		return fmt.Errorf("failed to create proxy: %v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if strings.EqualFold(host, "fly-app-controller") {
			log.Printf("[supervisor] Routing to admin interface for host: %s", host)
			admin.ServeHTTP(w, r)
			return
		}
		log.Printf("[supervisor] Routing to proxy for host: %s", host)
		proxy.ServeHTTP(w, r)
	})

	mux := http.NewServeMux()
	mux.Handle("/", handler)

	log.Printf("Starting supervisor on %s, proxying to %s", *listenAddr, *targetAddr)
	return http.ListenAndServe(*listenAddr, mux)
}
