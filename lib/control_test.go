package lib

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdmin(t *testing.T) {
	// Create supervisor and admin
	s := NewSupervisor([]string{"echo", "test"})
	a := NewAdmin(s, "test-token")

	// Test initial status
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "fly-app-controller"
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	var status struct {
		Configured bool `json:"configured"`
		Running    bool `json:"running"`
	}
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if status.Configured {
		t.Error("Should not be configured initially")
	}
	if status.Running {
		t.Error("Should not be running initially")
	}

	// Test invalid config
	invalidConfig := `{"bucket": "", "access_key": "", "secret_key": ""}`
	req = httptest.NewRequest("POST", "/", bytes.NewBufferString(invalidConfig))
	req.Host = "fly-app-controller"
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status code %d, got %d", http.StatusBadRequest, w.Code)
	}

	// Test valid config
	validConfig := ObjectStorageConfig{
		Bucket:    "test-bucket",
		Endpoint:  "http://localhost:9000",
		AccessKey: "test-key",
		SecretKey: "test-secret",
	}
	configJSON, _ := json.Marshal(validConfig)
	req = httptest.NewRequest("POST", "/", bytes.NewBuffer(configJSON))
	req.Host = "fly-app-controller"
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	// Check status after configuration
	req = httptest.NewRequest("GET", "/", nil)
	req.Host = "fly-app-controller"
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, req)

	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !status.Configured {
		t.Error("Should be configured after POST")
	}
	if !status.Running {
		t.Error("Should be running after configuration")
	}

	// Test wrong host
	req = httptest.NewRequest("GET", "/", nil)
	req.Host = "wrong-host"
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status code %d, got %d", http.StatusNotFound, w.Code)
	}
}

func TestAdminMethodNotAllowed(t *testing.T) {
	s := NewSupervisor([]string{"echo", "test"})
	a := NewAdmin(s, "test-token")

	// Test unsupported method
	req := httptest.NewRequest("PUT", "/", nil)
	req.Host = "fly-app-controller"
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status code %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}
}
