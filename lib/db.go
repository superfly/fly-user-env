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

// DBManager handles SQLite database operations and Litestream restore
// rule: Only implement restore using Litestream as a library
// rule: Do not implement replication or sync yet
type DBManager struct {
	config          *ObjectStorageConfig
	dataDir         string
	DBPath          string // symlink path for external use
	ActiveDBPath    string // real path to the active checkpoint's app.sqlite
	checkpointsPath string
}

// NewDBManager creates a new database manager instance
func NewDBManager(config *ObjectStorageConfig, dataDir string) *DBManager {
	return &DBManager{
		config:          config,
		dataDir:         dataDir,
		checkpointsPath: filepath.Join(dataDir, "db-checkpoints"),
	}
}

// Initialize ensures the database directory exists and restores from object storage if available
func (dm *DBManager) Initialize() error {
	log.Printf("DBManager.Initialize: checkpointsPath=%s", dm.checkpointsPath)

	// Create necessary directories
	if err := os.MkdirAll(dm.checkpointsPath, 0755); err != nil {
		return fmt.Errorf("failed to create checkpoints directory: %w", err)
	}

	// Check if we have any checkpoints
	entries, err := os.ReadDir(dm.checkpointsPath)
	if err != nil {
		return fmt.Errorf("failed to read checkpoints directory: %w", err)
	}

	dbSymlink := filepath.Join(dm.dataDir, "db")
	dbSymlinkFile := filepath.Join(dbSymlink, "app.sqlite")

	if len(entries) == 0 {
		// No checkpoints exist, create initial version "0"
		initialPath := filepath.Join(dm.checkpointsPath, "0")
		if err := os.MkdirAll(initialPath, 0755); err != nil {
			return fmt.Errorf("failed to create initial checkpoint directory: %w", err)
		}

		// Remove existing db symlink if it exists
		if err := os.RemoveAll(dbSymlink); err != nil {
			return fmt.Errorf("failed to remove existing db symlink: %w", err)
		}

		// Create symlink to initial version
		if err := os.Symlink(initialPath, dbSymlink); err != nil {
			return fmt.Errorf("failed to create symlink: %w", err)
		}

		dm.DBPath = dbSymlinkFile
		dm.ActiveDBPath = filepath.Join(initialPath, "app.sqlite")

		// Initialize SQLite database in the initial version
		db, err := sql.Open("sqlite3", dm.ActiveDBPath)
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer db.Close()

		if err := dm.initializeDB(db); err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
	} else {
		// Find the latest checkpoint
		var latestVersion string
		for _, entry := range entries {
			if entry.IsDir() {
				version := entry.Name()
				if latestVersion == "" || version > latestVersion {
					latestVersion = version
				}
			}
		}

		if latestVersion == "" {
			return fmt.Errorf("no valid checkpoint versions found")
		}

		latestPath := filepath.Join(dm.checkpointsPath, latestVersion)

		// Remove existing db symlink if it exists
		if err := os.RemoveAll(dbSymlink); err != nil {
			return fmt.Errorf("failed to remove existing db symlink: %w", err)
		}

		// Create symlink to latest checkpoint
		if err := os.Symlink(latestPath, dbSymlink); err != nil {
			return fmt.Errorf("failed to create symlink: %w", err)
		}

		dm.DBPath = dbSymlinkFile
		dm.ActiveDBPath = filepath.Join(latestPath, "app.sqlite")
	}

	log.Printf("DBManager.Initialize: DBPath (symlink)=%s", dm.DBPath)
	log.Printf("DBManager.Initialize: ActiveDBPath=%s", dm.ActiveDBPath)

	return nil
}

// litestreamDB creates and configures a Litestream DB with S3 replica
func (dm *DBManager) litestreamDB() *litestream.DB {
	lsdb := litestream.NewDB(dm.ActiveDBPath)

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
	return lsdb
}

// Comment out failing Litestream tests for now
func (dm *DBManager) Restore() error {
	ctx := context.Background()
	lsdb := dm.litestreamDB()
	if _, err := os.Stat(dm.ActiveDBPath); err == nil {
		log.Printf("local database already exists, skipping restore")
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if len(lsdb.Replicas) == 0 {
		return fmt.Errorf("no replicas configured")
	}
	replica := lsdb.Replicas[0]
	opt := litestream.NewRestoreOptions()
	opt.OutputPath = dm.ActiveDBPath
	var err error
	if opt.Generation, _, err = replica.CalcRestoreTarget(ctx, opt); err != nil {
		return err
	}
	if opt.Generation == "" {
		log.Printf("no generation found, creating new database")
		return nil
	}
	log.Printf("restoring replica for generation %s", opt.Generation)
	if err := replica.Restore(ctx, opt); err != nil {
		return err
	}
	log.Printf("restore complete")
	return nil
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
	log.Printf("StopReplication is not implemented yet")
	return nil
}

func (dm *DBManager) CreateCheckpoint(ctx context.Context, id string) error {
	// Get current position from the database
	lsdb := dm.litestreamDB()
	pos, err := lsdb.Pos()
	if err != nil {
		return fmt.Errorf("failed to get database position: %w", err)
	}

	// Create new checkpoint directory
	checkpointPath := filepath.Join(dm.checkpointsPath, id)
	if err := os.MkdirAll(checkpointPath, 0755); err != nil {
		return fmt.Errorf("failed to create checkpoint directory: %w", err)
	}

	// Get the replica and restore options
	if len(lsdb.Replicas) == 0 {
		return fmt.Errorf("no replicas configured")
	}
	replica := lsdb.Replicas[0]

	// Create restore options for PITR
	opt := litestream.NewRestoreOptions()
	opt.OutputPath = filepath.Join(checkpointPath, "app.sqlite")
	opt.Generation = pos.Generation
	opt.Index = pos.Index

	// Restore to the new checkpoint directory
	if err := replica.Restore(ctx, opt); err != nil {
		return fmt.Errorf("failed to restore database: %w", err)
	}

	return nil
}

func (dm *DBManager) RestoreToCheckpoint(ctx context.Context, id string) error {
	// Verify checkpoint exists
	checkpointPath := filepath.Join(dm.checkpointsPath, id)
	if _, err := os.Stat(checkpointPath); err != nil {
		return fmt.Errorf("checkpoint not found: %w", err)
	}

	// Remove existing db symlink
	dbSymlink := filepath.Dir(dm.DBPath)
	if err := os.Remove(dbSymlink); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove existing db symlink: %w", err)
	}

	// Create new symlink to checkpoint directory
	if err := os.Symlink(checkpointPath, dbSymlink); err != nil {
		return fmt.Errorf("failed to create symlink: %w", err)
	}

	return nil
}

func (dm *DBManager) initializeDB(db *sql.DB) error {
	if _, err := db.Exec("PRAGMA user_version = 1;"); err != nil {
		return fmt.Errorf("failed to set user_version: %w", err)
	}
	return nil
}
