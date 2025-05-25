package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"supervisor/lib"
)

func TestJuiceFSIntegration(t *testing.T) {
	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Fail fast if not running on Linux
	if runtime.GOOS != "linux" {
		t.Fatalf("JuiceFS integration test requires Linux environment (current OS: %s)", runtime.GOOS)
	}

	// Fail if required env vars are not set
	if os.Getenv("FLY_TIGRIS_BUCKET") == "" ||
		os.Getenv("FLY_TIGRIS_ENDPOINT_URL") == "" ||
		os.Getenv("FLY_TIGRIS_ACCESS_KEY") == "" ||
		os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY") == "" {
		t.Fatal("Integration test requires FLY_TIGRIS_* environment variables to be set")
	}

	// Create test directory
	dir := filepath.Join("..", "tmp", "juicefs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}
	defer func() {
		// Remove test directory
		os.RemoveAll(dir)
	}()

	// Clean up the bucket before starting
	bucket := os.Getenv("FLY_TIGRIS_BUCKET")
	endpoint := os.Getenv("FLY_TIGRIS_ENDPOINT_URL")
	cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cleanupCancel()
	cleanupCmd := exec.CommandContext(cleanupCtx, "aws", "s3", "rm", fmt.Sprintf("s3://%s/juicefs/", bucket), "--recursive", "--endpoint-url", endpoint)
	cleanupCmd.Env = append(os.Environ(),
		"AWS_ACCESS_KEY_ID="+os.Getenv("FLY_TIGRIS_ACCESS_KEY"),
		"AWS_SECRET_ACCESS_KEY="+os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY"),
	)
	t.Logf("Running cleanup command: %s", cleanupCmd.String())
	cleanupCmd.Stdout = os.Stdout
	cleanupCmd.Stderr = os.Stderr
	if err := cleanupCmd.Run(); err != nil {
		t.Logf("Warning: Failed to clean up bucket: %v", err)
	}

	// Create control server with JuiceFS component
	components := []lib.StackComponent{
		lib.NewJuiceFSComponent(),
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
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := control.Shutdown(shutdownCtx); err != nil {
			t.Logf("Warning: Failed to shutdown control server: %v", err)
		}
	}()

	// Start status monitoring goroutine
	// statusCtx, statusCancel := context.WithCancel(context.Background())
	// defer statusCancel()
	// go func() {
	// 	ticker := time.NewTicker(5 * time.Second)
	// 	defer ticker.Stop()
	// 	for {
	// 		select {
	// 		case <-statusCtx.Done():
	// 			return
	// 		case <-ticker.C:
	// 			for i, comp := range components {
	// 				// Use a short timeout for status checks
	// 				checkCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	// 				status := comp.Status(checkCtx)
	// 				cancel()
	// 				statusJSON, _ := json.MarshalIndent(status, "", "  ")
	// 				t.Logf("Component %d status:\n%s", i, string(statusJSON))
	// 			}
	// 		}
	// 	}
	// }()

	// Configure the control server
	config := lib.SystemConfig{
		Storage: lib.ObjectStorageConfig{
			Bucket:    os.Getenv("FLY_TIGRIS_BUCKET"),
			Endpoint:  os.Getenv("FLY_TIGRIS_ENDPOINT_URL"),
			AccessKey: os.Getenv("FLY_TIGRIS_ACCESS_KEY"),
			SecretKey: os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY"),
			Region:    "auto",
			KeyPrefix: "test-juicefs/",
			EnvDir:    dir,
		},
		Stacks: []string{"juicefs"},
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	t.Run("Configure JuiceFS", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, "POST", server.URL, bytes.NewBuffer(configJSON))
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

	// Wait for JuiceFS to be ready
	activeDir := filepath.Join(dir, "juicefs", "active")
	mountDir := filepath.Join(dir, "juicefs")
	if _, err := os.Stat(activeDir); os.IsNotExist(err) {
		if err := os.MkdirAll(activeDir, 0755); err != nil {
			t.Fatalf("Failed to create active directory: %v", err)
		}
	}
	// Optionally, check juicefs status
	checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
	defer checkCancel()
	dbPath := filepath.Join(dir, "db", "juicefs.sqlite")
	statusCmd := exec.CommandContext(checkCtx, "juicefs", "status", fmt.Sprintf("sqlite3://%s", dbPath), mountDir)
	statusOutput, err := statusCmd.CombinedOutput()
	t.Logf("juicefs status output: %s", string(statusOutput))
	if err != nil {
		t.Fatalf("juicefs status error: %v", err)
	}

	t.Run("Test file operations", func(t *testing.T) {
		// Create the active directory if it doesn't exist
		if err := os.MkdirAll(activeDir, 0755); err != nil {
			t.Fatalf("Failed to create active directory: %v", err)
		}
	})

	t.Run("Test checkpointing", func(t *testing.T) {
		// Test checkpointing with ID
		checkpointID := "test-checkpoint"
		checkpointReq := struct {
			CheckpointID string `json:"checkpoint_id"`
		}{
			CheckpointID: checkpointID,
		}
		checkpointJSON, err := json.Marshal(checkpointReq)
		if err != nil {
			t.Fatalf("Failed to marshal checkpoint request: %v", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/checkpoint", bytes.NewBuffer(checkpointJSON))
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

		// Verify checkpoint exists
		checkpointFile := filepath.Join(dir, "juicefs", "checkpoints", checkpointID, "test.txt")
		if _, err := os.Stat(checkpointFile); err != nil {
			t.Fatalf("Checkpoint file not found: %v", err)
		}

		// Verify new active directory is empty
		testFile := filepath.Join(activeDir, "test.txt")
		if _, err := os.Stat(testFile); err == nil {
			t.Fatal("Active directory should be empty after checkpoint")
		}

		// Write new content to active
		if err := os.WriteFile(testFile, []byte("modified"), 0644); err != nil {
			t.Fatalf("Failed to write to new active directory: %v", err)
		}

		// Test restore
		restoreReq := struct {
			CheckpointID string `json:"checkpoint_id"`
		}{
			CheckpointID: checkpointID,
		}
		restoreJSON, err := json.Marshal(restoreReq)
		if err != nil {
			t.Fatalf("Failed to marshal restore request: %v", err)
		}

		req, err = http.NewRequestWithContext(ctx, "POST", server.URL+"/restore", bytes.NewBuffer(restoreJSON))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Host = "fly-app-controller"
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("Content-Type", "application/json")

		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
			body, _ := io.ReadAll(resp.Body)
			t.Logf("Response body: %s", body)
		}

		// Verify restore worked
		data, err := os.ReadFile(testFile)
		if err != nil {
			t.Fatalf("Failed to read test file after restore: %v", err)
		}
		if string(data) != "test" {
			t.Errorf("Expected file contents 'test' after restore, got '%s'", string(data))
		}
	})
}
