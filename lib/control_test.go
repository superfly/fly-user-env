package lib

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// MockComponent is a test implementation of StackComponent
type MockComponent struct{}

func (m *MockComponent) Setup(ctx context.Context, cfg *ObjectStorageConfig) error {
	return nil
}

func (m *MockComponent) Cleanup(ctx context.Context) error {
	return nil
}

func TestControl(t *testing.T) {
	// Create test directory
	tmpDir := t.TempDir()

	// Create supervisor for testing
	supervisor := NewSupervisor([]string{"tail", "-f", "/dev/null"}, SupervisorConfig{
		TimeoutStop:  5 * time.Second,
		RestartDelay: time.Second,
	})
	defer supervisor.StopProcess()

	// Create control with default components
	control := NewControl("localhost:8080", "test-token", "test-token", tmpDir, supervisor)

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		control.ServeHTTP(w, r)
	}))
	defer ts.Close()

	// Test initial status
	req, err := http.NewRequest("GET", ts.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "fly-app-controller"
	req.Header.Set("Authorization", "Bearer test-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to get status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var status map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("Failed to decode status response: %v", err)
	}

	// Verify initial status
	if status["configured"] != false {
		t.Errorf("Expected configured to be false initially, got %v", status["configured"])
	}
	if status["running"] != false {
		t.Errorf("Expected running to be false initially, got %v", status["running"])
	}
}

func TestControlMethodNotAllowed(t *testing.T) {
	// Create test directory
	tmpDir := t.TempDir()

	// Create supervisor for testing
	supervisor := NewSupervisor([]string{"tail", "-f", "/dev/null"}, SupervisorConfig{
		TimeoutStop:  5 * time.Second,
		RestartDelay: time.Second,
	})
	defer supervisor.StopProcess()

	// Create control with default components
	control := NewControl("localhost:8080", "test-token", "test-token", tmpDir, supervisor)

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		control.ServeHTTP(w, r)
	}))
	defer ts.Close()

	// Test method not allowed
	req, err := http.NewRequest("PUT", ts.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "fly-app-controller"
	req.Header.Set("Authorization", "Bearer test-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("Expected status 405, got %d", resp.StatusCode)
	}
}
