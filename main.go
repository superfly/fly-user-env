package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"fly-user-env/cmd"
	"fly-user-env/lib"
)

func main() {
	err, cleanup, supervisor := cmd.RunSupervisor()
	if err != nil {
		log.Printf("Error: %v", err)
		os.Exit(1)
	}

	// Create control with default components
	control := lib.NewControl("localhost:8080", "test-token", "test-token", "tmp", supervisor)

	// Create HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		control.ServeHTTP(w, r)
	})

	// Start server
	go func() {
		if err := http.ListenAndServe(":8080", mux); err != nil {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

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
		os.Exit(1)
	}
	log.Printf("Cleanup completed successfully")
}
