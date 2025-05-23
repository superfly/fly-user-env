package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"supervisor/lib"
)

func TestJuiceFSIntegration(t *testing.T) {
	// Skip if required env vars are not set
	if os.Getenv("FLY_TIGRIS_BUCKET") == "" ||
		os.Getenv("FLY_TIGRIS_ENDPOINT_URL") == "" ||
		os.Getenv("FLY_TIGRIS_ACCESS_KEY") == "" ||
		os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY") == "" {
		t.Skip("Skipping integration test. Set FLY_TIGRIS_* environment variables to run.")
	}

	// Create test directory
	dir := filepath.Join("..", "tmp", "juicefs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}
	defer os.RemoveAll(dir)

	config := &lib.ObjectStorageConfig{
		Bucket:    os.Getenv("FLY_TIGRIS_BUCKET"),
		Endpoint:  os.Getenv("FLY_TIGRIS_ENDPOINT_URL"),
		AccessKey: os.Getenv("FLY_TIGRIS_ACCESS_KEY"),
		SecretKey: os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY"),
		Region:    "auto",
		KeyPrefix: "test-juicefs/",
	}

	juicefsComponent := lib.NewJuiceFSComponent(dir)
	if err := juicefsComponent.Setup(context.Background(), config); err != nil {
		t.Fatalf("JuiceFSComponent setup failed: %v", err)
	}
	defer juicefsComponent.Cleanup(context.Background())

	// Verify SQLite database exists
	dbPath := filepath.Join(dir, "juicefs.sqlite")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("SQLite database not found: %v", err)
	}

	// Wait a bit for replication to start
	time.Sleep(2 * time.Second)

	// Test that we can write to the active directory
	testFile := filepath.Join(dir, "juicefs", "active", "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Test that we can read from the active directory
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("Failed to read test file: %v", err)
	}
	if string(data) != "test" {
		t.Errorf("Expected file contents 'test', got '%s'", string(data))
	}

	// Test checkpointing with ID
	checkpointID := "test-checkpoint"
	if _, err := juicefsComponent.CreateCheckpoint(context.Background(), checkpointID); err != nil {
		t.Fatalf("Failed to create checkpoint: %v", err)
	}

	// Verify checkpoint exists
	checkpointFile := filepath.Join(dir, "juicefs", "checkpoints", checkpointID, "test.txt")
	if _, err := os.Stat(checkpointFile); err != nil {
		t.Fatalf("Checkpoint file not found: %v", err)
	}

	// Verify new active directory is empty
	if _, err := os.Stat(testFile); err == nil {
		t.Fatal("Active directory should be empty after checkpoint")
	}

	// Write new content to active
	if err := os.WriteFile(testFile, []byte("modified"), 0644); err != nil {
		t.Fatalf("Failed to write to new active directory: %v", err)
	}

	// Test restore
	if err := juicefsComponent.RestoreToCheckpoint(context.Background(), checkpointID); err != nil {
		t.Fatalf("Failed to restore checkpoint: %v", err)
	}

	// Verify restore worked
	data, err = os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("Failed to read test file after restore: %v", err)
	}
	if string(data) != "test" {
		t.Errorf("Expected file contents 'test' after restore, got '%s'", string(data))
	}

	// Test checkpointing without ID (just remove active)
	if _, err := juicefsComponent.CreateCheckpoint(context.Background(), ""); err != nil {
		t.Fatalf("Failed to remove active directory: %v", err)
	}

	// Verify active directory is gone
	if _, err := os.Stat(testFile); err == nil {
		t.Fatal("Active directory should be removed")
	}
}
