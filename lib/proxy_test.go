package lib

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// mockStatusProvider implements StatusProvider for testing
type mockStatusProvider struct {
	running bool
}

func (m *mockStatusProvider) IsRunning() bool {
	return m.running
}

func TestProxy(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	// Create a mock status provider
	status := &mockStatusProvider{running: true}

	// Create the proxy
	proxy, err := New(server.URL[7:], status) // Remove "http://" prefix
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	// Test successful proxy
	t.Run("successful proxy", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
		}
		if w.Body.String() != "OK" {
			t.Errorf("Expected body 'OK', got '%s'", w.Body.String())
		}
	})

	// Test when service is not running
	t.Run("service not running", func(t *testing.T) {
		status.running = false
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)

		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("Expected status code %d, got %d", http.StatusServiceUnavailable, w.Code)
		}
	})
}

func TestUnixSocketProxy(t *testing.T) {
	// Create a temporary directory for the Unix socket
	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")

	// Create a Unix domain socket listener
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to create Unix socket: %v", err)
	}
	defer listener.Close()

	// Create a test server on the Unix socket
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("OK"))
		}),
	}
	go server.Serve(listener)
	defer server.Close()

	// Create a mock status provider
	status := &mockStatusProvider{running: true}

	// Create the proxy with Unix domain socket
	proxy, err := New("unix:"+socketPath, status)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	// Test successful proxy
	t.Run("successful unix socket proxy", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
		}
		if w.Body.String() != "OK" {
			t.Errorf("Expected body 'OK', got '%s'", w.Body.String())
		}
	})
}
