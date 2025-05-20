package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"net/http/httptest"
	"supervisor/lib"
)

func TestControlIntegration(t *testing.T) {
	// Skip if required env vars are not set
	if os.Getenv("FLY_TIGRIS_BUCKET") == "" ||
		os.Getenv("FLY_TIGRIS_ENDPOINT_URL") == "" ||
		os.Getenv("FLY_TIGRIS_ACCESS_KEY") == "" ||
		os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY") == "" {
		t.Skip("Skipping integration test. Set FLY_TIGRIS_* environment variables to run.")
	}

	// Create test directory
	dir := filepath.Join("..", "tmp", "control")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}
	defer os.RemoveAll(dir)

	// Create supervisor with a dummy command
	supervisor := lib.NewSupervisor([]string{"tail", "-f", "/dev/null"}, lib.SupervisorConfig{
		TimeoutStop:  5 * time.Second,
		RestartDelay: time.Second,
	})
	defer supervisor.StopProcess()

	// Create control instance
	control := lib.NewControlWithConfig("localhost:8080", "test-token", supervisor, filepath.Join(dir, "config.json"), dir)

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Host, "fly-app-controller") {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		control.ServeHTTP(w, r)
	}))
	defer ts.Close()

	// Test cases
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
		defer resp.Body.Close()

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

	t.Run("Configure with all stacks", func(t *testing.T) {
		// Test configuration with all stacks
		config := lib.SystemConfig{
			Storage: lib.ObjectStorageConfig{
				Bucket:    os.Getenv("FLY_TIGRIS_BUCKET"),
				Endpoint:  os.Getenv("FLY_TIGRIS_ENDPOINT_URL"),
				AccessKey: os.Getenv("FLY_TIGRIS_ACCESS_KEY"),
				SecretKey: os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY"),
				Region:    "auto",
			},
			Stacks: []string{"db", "leaser"},
		}

		configJSON, err := json.Marshal(config)
		if err != nil {
			t.Fatalf("Failed to marshal config: %v", err)
		}

		req, err := http.NewRequest("POST", ts.URL, bytes.NewBuffer(configJSON))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Host = "fly-app-controller"
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		// Verify configuration was saved
		if _, err := os.Stat(filepath.Join(dir, "config.json")); os.IsNotExist(err) {
			t.Error("Config file was not created")
		}
	})

	t.Run("Invalid stack configuration", func(t *testing.T) {
		// Test configuration with invalid stack
		config := lib.SystemConfig{
			Storage: lib.ObjectStorageConfig{
				Bucket:    os.Getenv("FLY_TIGRIS_BUCKET"),
				Endpoint:  os.Getenv("FLY_TIGRIS_ENDPOINT_URL"),
				AccessKey: os.Getenv("FLY_TIGRIS_ACCESS_KEY"),
				SecretKey: os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY"),
				Region:    "auto",
			},
			Stacks: []string{"invalid_stack"},
		}

		configJSON, err := json.Marshal(config)
		if err != nil {
			t.Fatalf("Failed to marshal config: %v", err)
		}

		req, err := http.NewRequest("POST", ts.URL, bytes.NewBuffer(configJSON))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Host = "fly-app-controller"
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("Expected status 500, got %d", resp.StatusCode)
		}
	})

	t.Run("Missing required fields", func(t *testing.T) {
		// Test configuration with missing required fields
		config := lib.SystemConfig{
			Storage: lib.ObjectStorageConfig{
				// Missing required fields
			},
			Stacks: []string{"db"},
		}

		configJSON, err := json.Marshal(config)
		if err != nil {
			t.Fatalf("Failed to marshal config: %v", err)
		}

		req, err := http.NewRequest("POST", ts.URL, bytes.NewBuffer(configJSON))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Host = "fly-app-controller"
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("Expected status 400, got %d", resp.StatusCode)
		}
	})
}
