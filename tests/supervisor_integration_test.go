package tests

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"supervisor/lib"
)

// Helper function to get object storage configuration
func getObjectStorageConfig() *lib.ObjectStorageConfig {
	return &lib.ObjectStorageConfig{
		Bucket:    os.Getenv("FLY_TIGRIS_BUCKET"),
		Endpoint:  os.Getenv("FLY_TIGRIS_ENDPOINT_URL"),
		AccessKey: os.Getenv("FLY_TIGRIS_ACCESS_KEY"),
		SecretKey: os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY"),
		Region:    "auto",
	}
}

func TestSupervisorIntegration(t *testing.T) {
	// Skip if required env vars are not set
	if os.Getenv("FLY_TIGRIS_BUCKET") == "" ||
		os.Getenv("FLY_TIGRIS_ENDPOINT_URL") == "" ||
		os.Getenv("FLY_TIGRIS_ACCESS_KEY") == "" ||
		os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY") == "" {
		t.Skip("Skipping integration test. Set FLY_TIGRIS_* environment variables to run.")
	}

	// Create test directories in ./tmp/
	scenarios := []string{"replication"}
	for _, scenario := range scenarios {
		dir := filepath.Join("..", "tmp", scenario)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create test directory %s: %v", dir, err)
		}
		defer os.RemoveAll(dir)
	}

	// Test full functionality in each scenario
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			dir := filepath.Join("..", "tmp", scenario)
			config := &lib.ObjectStorageConfig{
				Bucket:    os.Getenv("FLY_TIGRIS_BUCKET"),
				Endpoint:  os.Getenv("FLY_TIGRIS_ENDPOINT_URL"),
				AccessKey: os.Getenv("FLY_TIGRIS_ACCESS_KEY"),
				SecretKey: os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY"),
				Region:    "auto",
				KeyPrefix: "test-" + scenario + "/",
			}
			// Create DBManager with a unique tmpDir
			tmpDir := t.TempDir()
			// Ensure parent directory exists for DBPath
			dbPath, err := filepath.Abs(filepath.Join(dir, "app.sqlite"))
			if err != nil {
				t.Fatalf("Failed to get absolute path for DBPath: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
				t.Fatalf("Failed to create parent directory for DBPath: %v", err)
			}
			dm := lib.NewDBManager(config, tmpDir)
			dm.DBPath = dbPath

			// Test Initialize
			if err := dm.Initialize(); err != nil {
				t.Fatalf("Initialize failed: %v", err)
			}

			// Verify database file exists
			if _, err := os.Stat(dm.DBPath); os.IsNotExist(err) {
				t.Fatalf("Database file was not created at %s", dm.DBPath)
			}

			// Test basic SQLite operations
			db, err := sql.Open("sqlite3", dm.DBPath)
			if err != nil {
				t.Fatalf("Failed to open database: %v", err)
			}
			defer db.Close()

			// Create a test table
			_, err = db.Exec(`
				CREATE TABLE IF NOT EXISTS test_table (
					id INTEGER PRIMARY KEY,
					value TEXT,
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP
				)
			`)
			if err != nil {
				t.Fatalf("Failed to create test table: %v", err)
			}

			// Insert test data
			initialValue := "test-value-" + scenario
			_, err = db.Exec(`
				INSERT INTO test_table (value) VALUES (?)
			`, initialValue)
			if err != nil {
				t.Fatalf("Failed to insert test data: %v", err)
			}

			// Verify data was inserted
			var value string
			err = db.QueryRow("SELECT value FROM test_table WHERE id = 1").Scan(&value)
			if err != nil {
				t.Fatalf("Failed to query test data: %v", err)
			}
			if value != initialValue {
				t.Errorf("Expected value '%s', got '%s'", initialValue, value)
			}
		})
	}
}

func TestLeaserIntegration(t *testing.T) {
	// Skip if required env vars are not set
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
