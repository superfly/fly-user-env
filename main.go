package main

import (
	"log"
	"os"

	"supervisor/cmd"
)

func main() {
	if err := cmd.RunSupervisor(); err != nil {
		log.Printf("Error: %v", err)
		os.Exit(1)
	}
}
