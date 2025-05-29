package tests

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"fly-user-env/lib"
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

	// Create control server WITHOUT configuring it
	components := []lib.StackComponent{
		lib.NewLeaserComponent(),
		lib.NewDBManagerComponent(dir),
	}
	control := lib.NewControl(
		"localhost:8080",
		"localhost:8080",
		"test-token",
		dir,
		nil, // supervisor not needed for this test
		components...,
	)
	server := httptest.NewServer(control)
	defer server.Close()

	t.Run("Initial status", func(t *testing.T) {
		req, err := http.NewRequest("GET", server.URL, nil)
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

	// Now configure the control server
	config := lib.SystemConfig{
		Storage: lib.ObjectStorageConfig{
			Bucket:    os.Getenv("FLY_TIGRIS_BUCKET"),
			Endpoint:  os.Getenv("FLY_TIGRIS_ENDPOINT_URL"),
			AccessKey: os.Getenv("FLY_TIGRIS_ACCESS_KEY"),
			SecretKey: os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY"),
			Region:    "auto",
		},
		Stacks: []string{"leaser", "db"},
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	t.Run("Configure with all stacks", func(t *testing.T) {
		req, err := http.NewRequest("POST", server.URL, bytes.NewBuffer(configJSON))
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
			body, _ := io.ReadAll(resp.Body)
			t.Logf("Response body: %s", body)
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

		req, err := http.NewRequest("POST", server.URL, bytes.NewBuffer(configJSON))
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

		req, err := http.NewRequest("POST", server.URL, bytes.NewBuffer(configJSON))
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

func TestEnvironmentAndConfigFileConflict(t *testing.T) {
	// Skip if required env vars are not set
	if os.Getenv("FLY_TIGRIS_BUCKET") == "" ||
		os.Getenv("FLY_TIGRIS_ENDPOINT_URL") == "" ||
		os.Getenv("FLY_TIGRIS_ACCESS_KEY") == "" ||
		os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY") == "" {
		t.Skip("Skipping integration test. Set FLY_TIGRIS_* environment variables to run.")
	}

	// Create test directory
	dir := filepath.Join("..", "tmp", "control-conflict")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}
	defer os.RemoveAll(dir)

	// Create an existing config file
	config := lib.SystemConfig{
		Storage: lib.ObjectStorageConfig{
			Bucket:    "test-bucket",
			Endpoint:  "test-endpoint",
			AccessKey: "test-access-key",
			SecretKey: "test-secret-key",
			Region:    "test-region",
		},
		Stacks: []string{"db"},
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, configJSON, 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Set environment variables
	os.Setenv("FLY_STORAGE_BUCKET", "env-bucket")
	os.Setenv("FLY_STORAGE_ENDPOINT", "env-endpoint")
	os.Setenv("FLY_STORAGE_ACCESS_KEY", "env-access-key")
	os.Setenv("FLY_STORAGE_SECRET_KEY", "env-secret-key")
	os.Setenv("FLY_STORAGE_REGION", "env-region")
	os.Setenv("FLY_STACKS", "db,leaser")
	defer func() {
		os.Unsetenv("FLY_STORAGE_BUCKET")
		os.Unsetenv("FLY_STORAGE_ENDPOINT")
		os.Unsetenv("FLY_STORAGE_ACCESS_KEY")
		os.Unsetenv("FLY_STORAGE_SECRET_KEY")
		os.Unsetenv("FLY_STORAGE_REGION")
		os.Unsetenv("FLY_STACKS")
	}()

	// Create control server
	components := []lib.StackComponent{
		lib.NewLeaserComponent(),
		lib.NewDBManagerComponent(dir),
	}
	control := lib.NewControl(
		"localhost:8080",
		"localhost:8080",
		"test-token",
		dir,
		nil, // supervisor not needed for this test
		components...,
	)
	server := httptest.NewServer(control)
	defer server.Close()

	// Test that server returns error when both config sources exist
	req, err := http.NewRequest("GET", server.URL, nil)
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

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", resp.StatusCode)
	}

	// Verify error message
	var errorResponse struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&errorResponse); err != nil {
		t.Fatalf("Failed to decode error response: %v", err)
	}
	expectedError := "configuration conflict: both environment variables and config file exist"
	if errorResponse.Error != expectedError {
		t.Errorf("Expected error message '%s', got '%s'", expectedError, errorResponse.Error)
	}
}

func TestEnvironmentConfigOnly(t *testing.T) {
	// Skip if required env vars are not set
	if os.Getenv("FLY_TIGRIS_BUCKET") == "" ||
		os.Getenv("FLY_TIGRIS_ENDPOINT_URL") == "" ||
		os.Getenv("FLY_TIGRIS_ACCESS_KEY") == "" ||
		os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY") == "" {
		t.Skip("Skipping integration test. Set FLY_TIGRIS_* environment variables to run.")
	}

	// Create test directory
	dir := filepath.Join("..", "tmp", "control-env")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}
	defer os.RemoveAll(dir)

	// Set environment variables
	os.Setenv("FLY_STORAGE_BUCKET", "env-bucket")
	os.Setenv("FLY_STORAGE_ENDPOINT", "env-endpoint")
	os.Setenv("FLY_STORAGE_ACCESS_KEY", "env-access-key")
	os.Setenv("FLY_STORAGE_SECRET_KEY", "env-secret-key")
	os.Setenv("FLY_STORAGE_REGION", "env-region")
	os.Setenv("FLY_STACKS", "db,leaser")
	defer func() {
		os.Unsetenv("FLY_STORAGE_BUCKET")
		os.Unsetenv("FLY_STORAGE_ENDPOINT")
		os.Unsetenv("FLY_STORAGE_ACCESS_KEY")
		os.Unsetenv("FLY_STORAGE_SECRET_KEY")
		os.Unsetenv("FLY_STORAGE_REGION")
		os.Unsetenv("FLY_STACKS")
	}()

	// Create control server
	components := []lib.StackComponent{
		lib.NewLeaserComponent(),
		lib.NewDBManagerComponent(dir),
	}
	control := lib.NewControl(
		"localhost:8080",
		"localhost:8080",
		"test-token",
		dir,
		nil, // supervisor not needed for this test
		components...,
	)
	server := httptest.NewServer(control)
	defer server.Close()

	// Test that server is configured from environment
	req, err := http.NewRequest("GET", server.URL, nil)
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
		Configured bool     `json:"configured"`
		Running    bool     `json:"running"`
		Stacks     []string `json:"stacks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !status.Configured {
		t.Error("Expected configured to be true")
	}
	if len(status.Stacks) != 2 {
		t.Errorf("Expected 2 stacks, got %d", len(status.Stacks))
	}
	expectedStacks := map[string]bool{"db": true, "leaser": true}
	for _, stack := range status.Stacks {
		if !expectedStacks[stack] {
			t.Errorf("Unexpected stack: %s", stack)
		}
	}
}

func TestConfigFileOnly(t *testing.T) {
	// Skip if required env vars are not set
	if os.Getenv("FLY_TIGRIS_BUCKET") == "" ||
		os.Getenv("FLY_TIGRIS_ENDPOINT_URL") == "" ||
		os.Getenv("FLY_TIGRIS_ACCESS_KEY") == "" ||
		os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY") == "" {
		t.Skip("Skipping integration test. Set FLY_TIGRIS_* environment variables to run.")
	}

	// Create test directory
	dir := filepath.Join("..", "tmp", "control-file")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}
	defer os.RemoveAll(dir)

	// Create an existing config file
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

	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, configJSON, 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Create control server
	components := []lib.StackComponent{
		lib.NewLeaserComponent(),
		lib.NewDBManagerComponent(dir),
	}
	control := lib.NewControl(
		"localhost:8080",
		"localhost:8080",
		"test-token",
		dir,
		nil, // supervisor not needed for this test
		components...,
	)
	server := httptest.NewServer(control)
	defer server.Close()

	// Test that server is configured from file
	req, err := http.NewRequest("GET", server.URL, nil)
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
		Configured bool     `json:"configured"`
		Running    bool     `json:"running"`
		Stacks     []string `json:"stacks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !status.Configured {
		t.Error("Expected configured to be true")
	}
	if len(status.Stacks) != 2 {
		t.Errorf("Expected 2 stacks, got %d", len(status.Stacks))
	}
	expectedStacks := map[string]bool{"db": true, "leaser": true}
	for _, stack := range status.Stacks {
		if !expectedStacks[stack] {
			t.Errorf("Unexpected stack: %s", stack)
		}
	}
}
