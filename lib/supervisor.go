// Package lib provides core functionality for process supervision and HTTP proxying.
// It includes components for managing long-running processes, handling HTTP requests,
// and providing an admin interface for configuration.
package lib

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Supervisor manages a long-running process and provides status information.
// It handles process lifecycle, output redirection, and automatic restart on failure.
type Supervisor struct {
	command []string
	process struct {
		sync.RWMutex
		running bool
		cmd     *exec.Cmd
		pid     int
	}
}

// NewSupervisor creates a new supervisor instance for the given command.
// The command is specified as a slice of strings where the first element
// is the executable path and subsequent elements are arguments.
func NewSupervisor(command []string) *Supervisor {
	return &Supervisor{
		command: command,
	}
}

// IsRunning returns true if the supervised process is currently running.
// This method is safe to call from multiple goroutines.
func (s *Supervisor) IsRunning() bool {
	s.process.RLock()
	defer s.process.RUnlock()

	if !s.process.running || s.process.pid == 0 {
		return false
	}

	// Check if process exists by sending signal 0
	// This doesn't actually send a signal, just checks if process exists
	process, err := os.FindProcess(s.process.pid)
	if err != nil {
		return false
	}

	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// StartProcess starts the supervised process and sets up output handling.
// It returns an error if the process is already running or if starting fails.
// The process will be automatically restarted if it exits.
func (s *Supervisor) StartProcess() error {
	s.process.Lock()
	defer s.process.Unlock()

	if s.process.running {
		return fmt.Errorf("process is already running")
	}

	if len(s.command) == 0 {
		return fmt.Errorf("empty command")
	}

	cmd := exec.Command(s.command[0], s.command[1:]...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start process: %v", err)
	}

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			log.Printf("[PROCESS STDOUT] %s", scanner.Text())
		}
	}()
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("[PROCESS STDERR] %s", scanner.Text())
		}
	}()

	go func() {
		err := cmd.Wait()
		s.process.Lock()
		s.process.running = false
		s.process.cmd = nil
		s.process.pid = 0
		s.process.Unlock()
		if err != nil {
			log.Printf("Process exited with error: %v", err)
		} else {
			log.Printf("Process exited successfully")
		}
		time.Sleep(time.Second)
		if err := s.StartProcess(); err != nil {
			log.Printf("Failed to restart process: %v", err)
		}
	}()

	s.process.running = true
	s.process.cmd = cmd
	s.process.pid = cmd.Process.Pid
	log.Printf("Started process with PID %d: %v", s.process.pid, s.command)
	return nil
}

// StopProcess gracefully stops the supervised process.
// It first attempts a graceful shutdown with SIGTERM,
// then falls back to SIGKILL if the process doesn't terminate.
// Returns an error if stopping the process fails.
func (s *Supervisor) StopProcess() error {
	s.process.Lock()
	defer s.process.Unlock()

	if !s.process.running {
		return nil
	}

	if s.process.cmd != nil && s.process.cmd.Process != nil {
		if err := s.process.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("failed to kill process: %v", err)
		}
	}

	s.process.running = false
	s.process.cmd = nil
	s.process.pid = 0
	return nil
}
