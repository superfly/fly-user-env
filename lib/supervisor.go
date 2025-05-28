// Package lib provides core functionality for process supervision and HTTP proxying.
// It includes components for managing long-running processes, handling HTTP requests,
// and providing an admin interface for configuration.
package lib

import (
	"fmt"
	"io"
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
	config  SupervisorConfig
	process struct {
		sync.RWMutex
		running bool
		stopped bool // Flag to track if process was stopped intentionally
		cmd     *exec.Cmd
		pid     int
	}
}

// SupervisorConfig holds configuration for the supervisor.
type SupervisorConfig struct {
	// TimeoutStop is the time to wait for graceful shutdown before force killing.
	// Defaults to 90 seconds if not set (matching systemd's default).
	TimeoutStop time.Duration

	// RestartDelay is the time to wait before restarting a failed process.
	// Defaults to 100ms if not set (matching systemd's default).
	RestartDelay time.Duration
}

// NewSupervisor creates a new supervisor instance for the given command.
// The command is specified as a slice of strings where the first element
// is the executable path and subsequent elements are arguments.
func NewSupervisor(command []string, config SupervisorConfig) *Supervisor {
	// Set defaults if not specified
	if config.TimeoutStop == 0 {
		config.TimeoutStop = 90 * time.Second
	}
	if config.RestartDelay == 0 {
		config.RestartDelay = time.Second
	}

	return &Supervisor{
		command: command,
		config:  config,
	}
}

// NewSupervisorCmd creates a new supervisor for a pre-configured command.
// This is useful when you need to set up environment variables or other command
// configuration before supervision.
func NewSupervisorCmd(cmd *exec.Cmd, config SupervisorConfig) *Supervisor {
	// Set defaults if not specified
	if config.TimeoutStop == 0 {
		config.TimeoutStop = 90 * time.Second
	}
	if config.RestartDelay == 0 {
		config.RestartDelay = time.Second
	}

	return &Supervisor{
		command: cmd.Args,
		config:  config,
		process: struct {
			sync.RWMutex
			running bool
			stopped bool
			cmd     *exec.Cmd
			pid     int
		}{
			cmd: cmd,
		},
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
// The process will be automatically restarted if it exits unexpectedly.
func (s *Supervisor) StartProcess() error {
	s.process.Lock()
	defer s.process.Unlock()

	if s.process.running {
		return fmt.Errorf("process is already running")
	}

	if len(s.command) == 0 && s.process.cmd == nil {
		return fmt.Errorf("empty command")
	}

	var cmd *exec.Cmd
	if s.process.cmd != nil {
		cmd = s.process.cmd
	} else {
		cmd = exec.Command(s.command[0], s.command[1:]...)
	}

	// Forward child process stdout to parent's stdout
	cmd.Stdout = os.Stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start process: %v", err)
	}

	s.process.running = true
	s.process.stopped = false
	s.process.cmd = cmd
	s.process.pid = cmd.Process.Pid
	log.Printf("Started process with PID %d: %v", s.process.pid, s.command)

	go func() {
		err := cmd.Wait()
		s.process.Lock()
		s.process.running = false
		s.process.stopped = false
		s.process.cmd = nil
		s.process.pid = 0
		shouldRestart := !s.process.stopped
		s.process.stopped = false
		s.process.Unlock()
		if err != nil {
			log.Printf("Process exited with error: %v", err)
		} else {
			log.Printf("Process exited successfully")
		}
		if shouldRestart {
			time.Sleep(s.config.RestartDelay)
			if err := s.StartProcess(); err != nil {
				log.Printf("Failed to restart process: %v", err)
			}
		}
	}()

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

	// Mark that we're stopping the process intentionally
	s.process.stopped = true

	if s.process.cmd != nil && s.process.cmd.Process != nil {
		// Create a channel to wait for process exit
		done := make(chan error, 1)
		go func() {
			done <- s.process.cmd.Wait()
		}()

		// First try SIGTERM for graceful shutdown
		log.Printf("Sending SIGTERM to process %d", s.process.pid)
		if err := s.process.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return fmt.Errorf("failed to send SIGTERM: %v", err)
		}

		// Wait for process to exit or timeout
		select {
		case err := <-done:
			if err != nil {
				log.Printf("Process %d exited with error: %v", s.process.pid, err)
			} else {
				log.Printf("Process %d exited successfully", s.process.pid)
			}
		case <-time.After(s.config.TimeoutStop):
			// Process didn't exit in time, send SIGKILL
			log.Printf("Process %d did not exit within %v, sending SIGKILL",
				s.process.pid, s.config.TimeoutStop)
			if err := s.process.cmd.Process.Kill(); err != nil {
				return fmt.Errorf("failed to kill process: %v", err)
			}
			// Wait for the kill to take effect
			<-done
		}

		// Ensure process is cleaned up
		if s.process.cmd.Process != nil {
			s.process.cmd.Process.Release()
		}

		// Close any open pipes
		if s.process.cmd.Stdout != nil {
			if closer, ok := s.process.cmd.Stdout.(io.Closer); ok {
				closer.Close()
			}
		}
		if s.process.cmd.Stderr != nil {
			if closer, ok := s.process.cmd.Stderr.(io.Closer); ok {
				closer.Close()
			}
		}
	}

	s.process.running = false
	s.process.cmd = nil
	s.process.pid = 0
	return nil
}

// ForwardSignal sends the given signal to the supervised process if it is running.
func (s *Supervisor) ForwardSignal(sig os.Signal) error {
	s.process.RLock()
	defer s.process.RUnlock()

	if !s.process.running || s.process.cmd == nil || s.process.cmd.Process == nil {
		return nil
	}
	return s.process.cmd.Process.Signal(sig)
}
