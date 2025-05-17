package lib

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// ObjectStorageConfig represents the configuration for object storage
type ObjectStorageConfig struct {
	Bucket    string `json:"bucket"`
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

// Admin manages the admin interface and object storage configuration
type Admin struct {
	mu            sync.RWMutex
	storageConfig *ObjectStorageConfig
	supervisor    *Supervisor
	token         string
}

// NewAdmin creates a new admin instance
func NewAdmin(supervisor *Supervisor, token string) *Admin {
	return &Admin{
		supervisor: supervisor,
		token:      token,
	}
}

// ServeHTTP handles HTTP requests for the control interface
func (c *Admin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != "fly-app-controller" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	auth := r.Header.Get("Authorization")
	expected := "Bearer " + c.token
	if auth != expected {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodPost:
		c.handleConfig(w, r)
	case http.MethodGet:
		c.handleStatus(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleConfig processes POST requests to configure object storage
func (c *Admin) handleConfig(w http.ResponseWriter, r *http.Request) {
	var config ObjectStorageConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if config.Bucket == "" || config.AccessKey == "" || config.SecretKey == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	// Store the configuration
	c.mu.Lock()
	c.storageConfig = &config
	c.mu.Unlock()

	// Start the supervised process
	if err := c.supervisor.StartProcess(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to start process: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// handleStatus returns the current status of the controller
func (c *Admin) handleStatus(w http.ResponseWriter, r *http.Request) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	status := struct {
		Configured bool `json:"configured"`
		Running    bool `json:"running"`
	}{
		Configured: c.storageConfig != nil,
		Running:    c.supervisor.IsRunning(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// GetStorageConfig returns the current object storage configuration
func (c *Admin) GetStorageConfig() *ObjectStorageConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.storageConfig
}
