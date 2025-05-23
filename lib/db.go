package lib

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/benbjohnson/litestream"
	lss3 "github.com/benbjohnson/litestream/s3"
)

// DBManager handles SQLite database operations
type DBManager struct {
	config  *ObjectStorageConfig
	dataDir string
	DBPath  string         // path to the database file
	lsDB    *litestream.DB // single instance for replication
}

// NewDBManager creates a new database manager instance
func NewDBManager(config *ObjectStorageConfig, dataDir string) *DBManager {
	return &DBManager{
		config:  config,
		dataDir: dataDir,
		DBPath:  filepath.Join(dataDir, "db", "app.sqlite"),
	}
}

// Initialize ensures the database directory exists
func (dm *DBManager) Initialize() error {
	log.Printf("DBManager.Initialize: DBPath=%s", dm.DBPath)

	// Always create the parent directory for DBPath
	dbDir := filepath.Dir(dm.DBPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("failed to create database directory %s: %w", dbDir, err)
	}

	// Initialize SQLite database if it doesn't exist
	if _, err := os.Stat(dm.DBPath); os.IsNotExist(err) {
		db, err := sql.Open("sqlite3", dm.DBPath)
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer db.Close()

		if err := dm.initializeDB(db); err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
	}

	return nil
}

// litestreamDB returns the active Litestream DB instance, creating it if necessary
func (dm *DBManager) litestreamDB() *litestream.DB {
	if dm.lsDB == nil {
		lsdb := litestream.NewDB(dm.DBPath)

		// Configure S3 replica client
		client := lss3.NewReplicaClient()
		client.Bucket = dm.config.Bucket
		client.Endpoint = dm.config.Endpoint
		client.AccessKeyID = dm.config.AccessKey
		client.SecretAccessKey = dm.config.SecretKey
		client.Region = dm.config.Region
		client.ForcePathStyle = true // Use path-style addressing

		log.Printf("Configuring Litestream with endpoint=%s, access_key=%s, region=%s, path_style=%v",
			client.Endpoint, client.AccessKeyID, client.Region, client.ForcePathStyle)

		replica := litestream.NewReplica(lsdb, "s3")
		replica.Client = client
		lsdb.Replicas = append(lsdb.Replicas, replica)
		dm.lsDB = lsdb
	}
	return dm.lsDB
}

func (dm *DBManager) StartReplication() error {
	lsdb := dm.litestreamDB()
	if len(lsdb.Replicas) == 0 {
		return fmt.Errorf("no replicas configured")
	}
	if err := lsdb.Open(); err != nil {
		return fmt.Errorf("failed to start replication: %w", err)
	}
	log.Printf("Started Litestream replication")
	return nil
}

func (dm *DBManager) StopReplication() error {
	lsdb := dm.litestreamDB()
	if err := lsdb.Close(context.Background()); err != nil {
		return fmt.Errorf("failed to stop replication: %w", err)
	}
	log.Printf("Stopped Litestream replication")
	return nil
}

func (dm *DBManager) initializeDB(db *sql.DB) error {
	// Set user version to ensure file exists
	if _, err := db.Exec("PRAGMA user_version = 1;"); err != nil {
		return fmt.Errorf("failed to set user_version: %w", err)
	}
	return nil
}
