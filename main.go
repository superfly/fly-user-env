package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"supervisor/cmd"
)

func main() {
	err, cleanup, supervisor := cmd.RunSupervisor()
	if err != nil {
		log.Printf("Error: %v", err)
		os.Exit(1)
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
		os.Exit(1)
	}
	log.Printf("Cleanup completed successfully")
}
