package lib

import (
	"testing"
	"time"
)

func TestSupervisor(t *testing.T) {
	t.Log("Starting TestSupervisor")
	// Use a long-running command
	s := NewSupervisor([]string{"tail", "-f", "/dev/null"}, SupervisorConfig{
		TimeoutStop:  5 * time.Second,
		RestartDelay: time.Second,
	})
	defer func() {
		t.Log("Cleaning up TestSupervisor")
		if err := s.StopProcess(); err != nil {
			t.Logf("Error during cleanup: %v", err)
		}
	}()

	// Test initial state
	if s.IsRunning() {
		t.Error("Supervisor should not be running initially")
	}

	// Test starting process
	t.Log("Starting process")
	if err := s.StartProcess(); err != nil {
		t.Errorf("Failed to start process: %v", err)
	}

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)

	if !s.IsRunning() {
		t.Error("Process should be running after start")
	}

	// Test stopping process
	t.Log("Stopping process")
	if err := s.StopProcess(); err != nil {
		t.Errorf("Failed to stop process: %v", err)
	}

	// Test that process is not running
	if s.IsRunning() {
		t.Error("Process should not be running after stop")
	}
	t.Log("TestSupervisor completed")
}

func TestSupervisorRestart(t *testing.T) {
	t.Log("Starting TestSupervisorRestart")
	// Use a command that will exit after a short time
	s := NewSupervisor([]string{"sleep", "1"}, SupervisorConfig{
		TimeoutStop:  5 * time.Second,
		RestartDelay: time.Second,
	})
	defer func() {
		t.Log("Cleaning up TestSupervisorRestart")
		if err := s.StopProcess(); err != nil {
			t.Logf("Error during cleanup: %v", err)
		}
	}()

	// Start process
	t.Log("Starting process")
	if err := s.StartProcess(); err != nil {
		t.Errorf("Failed to start process: %v", err)
	}

	// Give it a moment to start
	t.Log("Waiting for process to start")
	for i := 0; i < 5; i++ {
		time.Sleep(200 * time.Millisecond)
		if s.IsRunning() {
			t.Log("Process is running")
			break
		}
		if i == 4 {
			t.Error("Process failed to start within timeout")
		}
	}

	if !s.IsRunning() {
		t.Error("Process should be running after start")
	}

	// Wait for process to exit naturally
	t.Log("Waiting for process to exit")
	time.Sleep(2 * time.Second)

	// Wait for restart
	t.Log("Waiting for process to restart")
	for i := 0; i < 5; i++ {
		time.Sleep(200 * time.Millisecond)
		if s.IsRunning() {
			t.Log("Process has restarted")
			break
		}
		if i == 4 {
			t.Error("Process failed to restart within timeout")
		}
	}

	if !s.IsRunning() {
		t.Error("Process should be running after restart")
	}

	// Stop process
	t.Log("Stopping process")
	if err := s.StopProcess(); err != nil {
		t.Errorf("Failed to stop process: %v", err)
	}
	t.Log("TestSupervisorRestart completed")
}
