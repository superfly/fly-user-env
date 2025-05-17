package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"supervisor/lib"
)

func TestSupervisorIntegration(t *testing.T) {
	// Set up test environment
	os.Setenv("CONTROLLER_TOKEN", "test-token")
	defer os.Unsetenv("CONTROLLER_TOKEN")

	// Start a Go HTTP server as the upstream
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer upstream.Close()

	// Extract host:port from upstream.URL
	u, err := net.ResolveTCPAddr("tcp", strings.TrimPrefix(upstream.URL, "http://"))
	if err != nil {
		t.Fatalf("Failed to parse upstream URL: %v", err)
	}
	upstreamAddr := fmt.Sprintf("localhost:%d", u.Port)

	// Create supervisor with a dummy command (simulate process management)
	supervisor := lib.NewSupervisor([]string{"tail", "-f", "/dev/null"})
	admin := lib.NewAdmin(supervisor, "test-token")
	proxy, err := lib.New(upstreamAddr, supervisor)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if strings.EqualFold(host, "fly-app-controller") {
			admin.ServeHTTP(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer ts.Close()

	// Test 1: Process should not be running initially
	t.Run("Process not running initially", func(t *testing.T) {
		if supervisor.IsRunning() {
			t.Error("Process should not be running initially")
		}
	})

	// Test 2: Proxy should return 503 before process starts
	t.Run("Proxy returns 503 before process starts", func(t *testing.T) {
		resp, err := http.Get(ts.URL)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("Expected status 503, got %d", resp.StatusCode)
		}
	})

	// Test 3: Configure object storage
	t.Run("Configure object storage", func(t *testing.T) {
		config := lib.ObjectStorageConfig{
			Bucket:    "test-bucket",
			Endpoint:  "http://localhost:9000",
			AccessKey: "test-key",
			SecretKey: "test-secret",
		}
		configJSON, _ := json.Marshal(config)

		req, err := http.NewRequest("POST", ts.URL, bytes.NewBuffer(configJSON))
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
	})

	// Test 4: Process should be running after configuration
	t.Run("Process running after configuration", func(t *testing.T) {
		if !supervisor.IsRunning() {
			t.Error("Process should be running after configuration")
		}
	})

	// Test 5: Proxy should now work
	t.Run("Proxy works after process starts", func(t *testing.T) {
		resp, err := http.Get(ts.URL)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}
	})

	// Test 6: Status endpoint should show running
	t.Run("Status endpoint shows running", func(t *testing.T) {
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
		if !status.Configured {
			t.Error("Status should show configured")
		}
		if !status.Running {
			t.Error("Status should show running")
		}
	})
}
