package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"fly-user-env/lib"
)

func TestLeaserHTTPIntegration(t *testing.T) {
	// Skip if required env vars are not set
	if os.Getenv("FLY_TIGRIS_BUCKET") == "" ||
		os.Getenv("FLY_TIGRIS_ENDPOINT_URL") == "" ||
		os.Getenv("FLY_TIGRIS_ACCESS_KEY") == "" ||
		os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY") == "" {
		t.Skip("Skipping integration test. Set FLY_TIGRIS_* environment variables to run.")
	}

	// Create a temporary directory for the test
	dir, err := os.MkdirTemp("", "leaser-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	// Create the control server with leaser component
	control := lib.NewControl(
		"localhost:8080",
		"localhost:8080",
		"test-token",
		dir,
		nil,
		lib.NewLeaserComponent(),
	)

	// Configure the control server
	config := lib.SystemConfig{
		Storage: lib.ObjectStorageConfig{
			Bucket:    os.Getenv("FLY_TIGRIS_BUCKET"),
			Endpoint:  os.Getenv("FLY_TIGRIS_ENDPOINT_URL"),
			AccessKey: os.Getenv("FLY_TIGRIS_ACCESS_KEY"),
			SecretKey: os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY"),
			Region:    "auto",
		},
		Stacks: []string{"leaser"},
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	// Configure the server
	req, err := http.NewRequest("POST", "/", bytes.NewBuffer(configJSON))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Host = "fly-app-controller"
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	control.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
		body, _ := io.ReadAll(w.Body)
		t.Logf("Response body: %s", body)
	}

	t.Run("Release lease endpoint", func(t *testing.T) {
		// Create a request to release leases
		req, err := http.NewRequest("POST", "/stack/leaser/release", nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Host = "fly-app-controller"
		req.Header.Set("Authorization", "Bearer test-token")

		w := httptest.NewRecorder()
		control.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
			body, _ := io.ReadAll(w.Body)
			t.Logf("Response body: %s", body)
		}
	})

	t.Run("Invalid path", func(t *testing.T) {
		// Test with invalid path
		req, err := http.NewRequest("POST", "/stack/leaser/invalid", nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Host = "fly-app-controller"
		req.Header.Set("Authorization", "Bearer test-token")

		w := httptest.NewRecorder()
		control.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("Expected status 404, got %d", w.Code)
		}
	})

	t.Run("Invalid method", func(t *testing.T) {
		// Test with invalid HTTP method
		req, err := http.NewRequest("GET", "/stack/leaser/release", nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Host = "fly-app-controller"
		req.Header.Set("Authorization", "Bearer test-token")

		w := httptest.NewRecorder()
		control.ServeHTTP(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected status 405, got %d", w.Code)
		}
	})
}

// Component-level leaser test moved from supervisor_integration_test.go
func TestLeaserComponentDirect(t *testing.T) {
	if os.Getenv("FLY_TIGRIS_BUCKET") == "" ||
		os.Getenv("FLY_TIGRIS_ENDPOINT_URL") == "" ||
		os.Getenv("FLY_TIGRIS_ACCESS_KEY") == "" ||
		os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY") == "" {
		t.Skip("Skipping integration test. Set FLY_TIGRIS_* environment variables to run.")
	}

	config := &lib.ObjectStorageConfig{
		Bucket:    os.Getenv("FLY_TIGRIS_BUCKET"),
		Endpoint:  os.Getenv("FLY_TIGRIS_ENDPOINT_URL"),
		AccessKey: os.Getenv("FLY_TIGRIS_ACCESS_KEY"),
		SecretKey: os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY"),
		Region:    "auto",
		KeyPrefix: "test-leaser/",
	}

	leaserComponent := lib.NewLeaserComponent()
	if err := leaserComponent.Setup(context.Background(), config, ""); err != nil {
		t.Fatalf("LeaserComponent setup failed: %v", err)
	}
	defer leaserComponent.Cleanup(context.Background())

	if leaserComponent.Leaser == nil {
		t.Fatal("Leaser is nil after setup")
	}
	lease, err := leaserComponent.Leaser.AcquireLease(context.Background())
	if err != nil {
		t.Fatalf("Failed to acquire lease: %v", err)
	}
	if err := leaserComponent.Leaser.ReleaseLease(context.Background(), lease.Epoch); err != nil {
		t.Fatalf("Failed to release lease: %v", err)
	}
}
