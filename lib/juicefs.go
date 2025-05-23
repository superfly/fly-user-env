package lib

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/benbjohnson/litestream"
)

// JuiceFSComponent implements StackComponent and CheckpointableComponent for JuiceFS file system management
type JuiceFSComponent struct {
	config    *ObjectStorageConfig
	activeDir string
	dbManager *DBManager
}

// NewJuiceFSComponent creates a new JuiceFS component
func NewJuiceFSComponent() *JuiceFSComponent {
	return &JuiceFSComponent{}
}

// Setup initializes the JuiceFS component with the given config
func (j *JuiceFSComponent) Setup(ctx context.Context, cfg *ObjectStorageConfig) error {
	j.config = cfg

	// Create mount directory
	mountDir := filepath.Join(cfg.EnvDir, "juicefs")
	if err := os.MkdirAll(mountDir, 0755); err != nil {
		return fmt.Errorf("failed to create mount directory: %w", err)
	}

	// Initialize SQLite database for metadata
	dbPath := filepath.Join(cfg.EnvDir, "db", "juicefs.sqlite")
	j.dbManager = NewDBManager(cfg, filepath.Join(cfg.EnvDir, "db"))
	j.dbManager.DBPath = dbPath
	if err := j.dbManager.Initialize(); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	if err := j.dbManager.StartReplication(); err != nil {
		return fmt.Errorf("failed to start replication: %w", err)
	}

	// Format the filesystem if it doesn't exist
	formatCmd := exec.CommandContext(ctx, "juicefs", "format",
		"--storage", "s3",
		"--bucket", cfg.Bucket,
		"--access-key", cfg.AccessKey,
		"--secret-key", cfg.SecretKey,
		"--endpoint", cfg.Endpoint,
		"--region", cfg.Region,
		"--meta", dbPath,
		filepath.Join(cfg.KeyPrefix, "juicefs"),
		"juicefs")

	if err := formatCmd.Run(); err != nil {
		return fmt.Errorf("failed to format JuiceFS: %w", err)
	}

	// Mount the filesystem
	mountCmd := exec.CommandContext(ctx, "juicefs", "mount",
		"--storage", "s3",
		"--bucket", cfg.Bucket,
		"--access-key", cfg.AccessKey,
		"--secret-key", cfg.SecretKey,
		"--endpoint", cfg.Endpoint,
		"--region", cfg.Region,
		"--meta", dbPath,
		filepath.Join(cfg.KeyPrefix, "juicefs"),
		mountDir)

	if err := mountCmd.Start(); err != nil {
		return fmt.Errorf("failed to start JuiceFS mount: %w", err)
	}

	// Set active directory path
	j.activeDir = filepath.Join(mountDir, "active")

	// Create active and checkpoints directories within the mount
	if err := os.MkdirAll(j.activeDir, 0755); err != nil {
		return fmt.Errorf("failed to create active directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(mountDir, "checkpoints"), 0755); err != nil {
		return fmt.Errorf("failed to create checkpoints directory: %w", err)
	}

	return nil
}

// Cleanup performs cleanup when the component is no longer needed
func (j *JuiceFSComponent) Cleanup(ctx context.Context) error {
	if j.dbManager != nil {
		if err := j.dbManager.StopReplication(); err != nil {
			return fmt.Errorf("failed to stop replication: %w", err)
		}
	}
	return nil
}

// CreateCheckpoint creates a checkpoint by moving the active directory to a new checkpoint directory
func (j *JuiceFSComponent) CreateCheckpoint(ctx context.Context, id string) (string, error) {
	if id == "" {
		// If no ID provided, just remove active
		if err := os.RemoveAll(j.activeDir); err != nil {
			return "", fmt.Errorf("failed to remove active directory: %w", err)
		}
		return "", nil
	}

	checkpointDir := filepath.Join(filepath.Dir(j.activeDir), "checkpoints", id)

	// Move active to checkpoint
	if err := os.Rename(j.activeDir, checkpointDir); err != nil {
		return "", fmt.Errorf("failed to move active to checkpoint: %w", err)
	}

	// Create new active directory
	if err := os.MkdirAll(j.activeDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create new active directory: %w", err)
	}

	return id, nil
}

// RestoreToCheckpoint restores the filesystem to a previous checkpoint
func (j *JuiceFSComponent) RestoreToCheckpoint(ctx context.Context, id string) error {
	checkpointDir := filepath.Join(filepath.Dir(j.activeDir), "checkpoints", id)

	// Remove current active
	if err := os.RemoveAll(j.activeDir); err != nil {
		return fmt.Errorf("failed to remove active directory: %w", err)
	}

	// Move checkpoint to active
	if err := os.Rename(checkpointDir, j.activeDir); err != nil {
		return fmt.Errorf("failed to move checkpoint to active: %w", err)
	}

	// Restore SQLite database from litestream
	if j.dbManager != nil {
		dbPath := filepath.Join(j.config.EnvDir, "db", "juicefs.sqlite")
		lsdb := j.dbManager.litestreamDB()
		if lsdb != nil && len(lsdb.Replicas) > 0 {
			// Use the first replica for restoration
			replica := lsdb.Replicas[0]
			opt := litestream.RestoreOptions{
				OutputPath: dbPath,
				// If id is empty, restore to latest
				// Otherwise, use the checkpoint ID as the generation
				Generation: id,
			}
			if err := replica.Restore(ctx, opt); err != nil {
				return fmt.Errorf("failed to restore SQLite database: %w", err)
			}
		}
	}

	return nil
}
