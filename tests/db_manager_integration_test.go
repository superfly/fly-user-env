package tests

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"fly-user-env/lib"
)

func TestDBManagerIntegration(t *testing.T) {
	// Check for required environment variables
	requiredEnvVars := []string{
		"FLY_TIGRIS_BUCKET",
		"FLY_TIGRIS_ENDPOINT_URL",
		"FLY_TIGRIS_ACCESS_KEY",
		"FLY_TIGRIS_SECRET_ACCESS_KEY",
	}

	for _, envVar := range requiredEnvVars {
		if os.Getenv(envVar) == "" {
			t.Skipf("Skipping test: %s environment variable not set", envVar)
		}
	}

	// Create test directory
	testDir := filepath.Join(t.TempDir(), "test-db")
	require.NoError(t, os.MkdirAll(testDir, 0755))

	// Initialize DBManager
	config := &lib.ObjectStorageConfig{
		Bucket:    os.Getenv("FLY_TIGRIS_BUCKET"),
		Endpoint:  os.Getenv("FLY_TIGRIS_ENDPOINT_URL"),
		AccessKey: os.Getenv("FLY_TIGRIS_ACCESS_KEY"),
		SecretKey: os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY"),
		Region:    "us-east-1",
	}

	dm := lib.NewDBManager(config, testDir)
	require.NoError(t, dm.Initialize())

	// Test basic database operations
	db, err := sql.Open("sqlite3", dm.DBPath)
	require.NoError(t, err)
	defer db.Close()

	// Create test table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS test (
			id INTEGER PRIMARY KEY,
			value TEXT
		)
	`)
	require.NoError(t, err)

	// Insert test data
	_, err = db.Exec("INSERT INTO test (value) VALUES (?)", "test-value")
	require.NoError(t, err)

	// Verify data
	var value string
	err = db.QueryRow("SELECT value FROM test WHERE id = 1").Scan(&value)
	require.NoError(t, err)
	assert.Equal(t, "test-value", value)

	// Test replication
	require.NoError(t, dm.StartReplication())
	require.NoError(t, dm.StopReplication())
}
