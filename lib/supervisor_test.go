package lib

import (
	"testing"
	"time"
)

func TestSupervisor(t *testing.T) {
	// Use a long-running command
	s := NewSupervisor([]string{"tail", "-f", "/dev/null"})
	defer s.StopProcess() // Ensure process is stopped after test

	// Test initial state
	if s.IsRunning() {
		t.Error("Supervisor should not be running initially")
	}

	// Test starting process
	if err := s.StartProcess(); err != nil {
		t.Errorf("Failed to start process: %v", err)
	}

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)

	if !s.IsRunning() {
		t.Error("Process should be running after start")
	}

	// Test stopping process
	if err := s.StopProcess(); err != nil {
		t.Errorf("Failed to stop process: %v", err)
	}

	// Test that process is not running
	if s.IsRunning() {
		t.Error("Process should not be running after stop")
	}
}

func TestSupervisorRestart(t *testing.T) {
	// Use a long-running command
	s := NewSupervisor([]string{"tail", "-f", "/dev/null"})
	defer s.StopProcess() // Ensure process is stopped after test

	// Start process
	if err := s.StartProcess(); err != nil {
		t.Errorf("Failed to start process: %v", err)
	}

	// Give it a moment to start
	for i := 0; i < 5; i++ {
		time.Sleep(200 * time.Millisecond)
		if s.IsRunning() {
			break
		}
	}

	if !s.IsRunning() {
		t.Error("Process should be running after start")
	}

	// Simulate process exit by stopping it
	if err := s.StopProcess(); err != nil {
		t.Errorf("Failed to stop process: %v", err)
	}

	// Wait for restart
	for i := 0; i < 5; i++ {
		time.Sleep(200 * time.Millisecond)
		if s.IsRunning() {
			break
		}
	}

	if !s.IsRunning() {
		t.Error("Process should be running after restart")
	}

	// Stop process
	if err := s.StopProcess(); err != nil {
		t.Errorf("Failed to stop process: %v", err)
	}
}
