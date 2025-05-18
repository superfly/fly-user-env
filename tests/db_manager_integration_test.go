package tests

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"supervisor/lib"

	_ "github.com/mattn/go-sqlite3"
)

func TestDBManagerIntegration(t *testing.T) {
	// Skip if required env vars are not set
	if os.Getenv("FLY_TIGRIS_BUCKET") == "" ||
		os.Getenv("FLY_TIGRIS_ENDPOINT_URL") == "" ||
		os.Getenv("FLY_TIGRIS_ACCESS_KEY") == "" ||
		os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY") == "" {
		t.Skip("Skipping integration test. Set FLY_TIGRIS_* environment variables to run.")
	}

	// Create test directories in ./tmp/
	scenarios := []string{"no_db", "existing_db"}
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
			dm := lib.NewDBManager(config)
			dm.DBPath = filepath.Join(dir, "app.sqlite")

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
