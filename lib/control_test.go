package lib

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// MockComponent is a test implementation of StackComponent
type MockComponent struct{}

func (m *MockComponent) Setup(ctx context.Context, cfg *ObjectStorageConfig) error {
	return nil
}

func (m *MockComponent) Cleanup(ctx context.Context) error {
	return nil
}

func TestAdmin(t *testing.T) {
	// Create supervisor with a dummy command
	supervisor := NewSupervisor([]string{"tail", "-f", "/dev/null"})
	defer supervisor.StopProcess()

	// Create admin with default components
	admin := NewAdmin("localhost:8080", "test-token", supervisor)

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Host, "fly-app-controller") {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		admin.ServeHTTP(w, r)
	}))
	defer ts.Close()

	// Test initial status
	t.Run("Initial status", func(t *testing.T) {
		req, err := http.NewRequest("GET", ts.URL, nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Host = "fly-app-controller"
		req.Header.Set("Authorization", "Bearer test-token")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		var status struct {
			Configured bool `json:"configured"`
			Running    bool `json:"running"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}
		if status.Configured {
			t.Error("Expected configured to be false initially")
		}
		if status.Running {
			t.Error("Expected running to be false initially")
		}
	})

	// Test invalid configuration
	t.Run("Invalid configuration", func(t *testing.T) {
		req, err := http.NewRequest("POST", ts.URL, strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Host = "fly-app-controller"
		req.Header.Set("Authorization", "Bearer test-token")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("Expected status 400, got %d", resp.StatusCode)
		}
	})

	// Test wrong host
	t.Run("Wrong host", func(t *testing.T) {
		req, err := http.NewRequest("GET", ts.URL, nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Host = "wrong-host"
		req.Header.Set("Authorization", "Bearer test-token")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("Expected status 404, got %d", resp.StatusCode)
		}
	})
}

func TestAdminMethodNotAllowed(t *testing.T) {
	// Create supervisor with a dummy command
	supervisor := NewSupervisor([]string{"tail", "-f", "/dev/null"})
	defer supervisor.StopProcess()

	// Create admin with default components
	admin := NewAdmin("localhost:8080", "test-token", supervisor)

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Host, "fly-app-controller") {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		admin.ServeHTTP(w, r)
	}))
	defer ts.Close()

	// Test PUT method
	req, err := http.NewRequest("PUT", ts.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Host = "fly-app-controller"
	req.Header.Set("Authorization", "Bearer test-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("Expected status 405, got %d", resp.StatusCode)
	}
}
