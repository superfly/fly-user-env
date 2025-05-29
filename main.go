package main

import (
	"flag"
	"log"
	"os"

	"fly-user-env/cmd"
)

func main() {
	// Parse command
	flag.Parse()
	args := flag.Args()

	// Default to server command if no command specified
	if len(args) == 0 {
		args = []string{"server"}
	}

	// Dispatch command
	switch args[0] {
	case "server":
		if err := cmd.RunServerAndWait(); err != nil {
			log.Printf("Error: %v", err)
			os.Exit(1)
		}

	default:
		log.Printf("Unknown command: %s", args[0])
		os.Exit(1)
	}
}
