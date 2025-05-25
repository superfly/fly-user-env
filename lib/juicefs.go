package lib

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

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
func (j *JuiceFSComponent) Setup(ctx context.Context, cfg *ObjectStorageConfig, juicefsPath string) error {
	j.config = cfg

	// Create mount directory
	mountDir := filepath.Join(cfg.EnvDir, "juicefs")
	if err := os.MkdirAll(mountDir, 0755); err != nil {
		return fmt.Errorf("failed to create mount directory: %w", err)
	}

	// Create db directory
	dbDir := filepath.Join(cfg.EnvDir, "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("failed to create db directory: %w", err)
	}

	// Initialize SQLite database for metadata
	dbPath := filepath.Join(dbDir, "juicefs.sqlite")
	j.dbManager = NewDBManager(cfg, filepath.Join(cfg.EnvDir, "db"))
	j.dbManager.DBPath = dbPath
	if err := j.dbManager.Initialize(); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	if err := j.dbManager.StartReplication(); err != nil {
		return fmt.Errorf("failed to start replication: %w", err)
	}

	// Compose bucket as endpoint/bucket
	bucketURL := fmt.Sprintf("%s/%s", cfg.Endpoint, cfg.Bucket)

	// Format the filesystem if it doesn't exist
	formatCmd := exec.CommandContext(ctx, juicefsPath, "format",
		"--storage", "s3",
		"--bucket", bucketURL,
		"--trash-days", "0",
		fmt.Sprintf("sqlite3://%s", dbPath),
		"juicefs")

	// Set environment variables for authentication during format
	formatCmd.Env = append(os.Environ(),
		"AWS_ACCESS_KEY_ID="+cfg.AccessKey,
		"AWS_SECRET_ACCESS_KEY="+cfg.SecretKey,
	)

	// Capture format command output
	formatOutput, err := formatCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to format JuiceFS: %w\nOutput: %s", err, string(formatOutput))
	}
	fmt.Printf("JuiceFS format output: %s\n", string(formatOutput))

	// Mount the filesystem
	mountCmd := exec.CommandContext(ctx, juicefsPath, "mount",
		"--storage", "s3",
		"--bucket", bucketURL,
		fmt.Sprintf("sqlite3://%s", dbPath),
		mountDir,
	)

	// Set environment variables for authentication during mount
	mountCmd.Env = append(os.Environ(),
		"AWS_ACCESS_KEY_ID="+cfg.AccessKey,
		"AWS_SECRET_ACCESS_KEY="+cfg.SecretKey,
	)

	// Run the mount command and capture output
	mountOutput, err := mountCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to mount JuiceFS: %w\nOutput: %s", err, string(mountOutput))
	}
	fmt.Printf("JuiceFS mount output: %s\n", string(mountOutput))

	// Give it a moment to start
	time.Sleep(2 * time.Second)

	// Verify it's running
	statusCmd := exec.CommandContext(ctx, "juicefs", "status", fmt.Sprintf("sqlite3://%s", dbPath), mountDir)
	statusOutput, err := statusCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to verify JuiceFS mount: %w\nOutput: %s", err, string(statusOutput))
	}
	fmt.Printf("JuiceFS mount status: %s\n", string(statusOutput))

	// Set active directory path within the mount
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

	// Get the mount directory from the active directory path
	mountDir := filepath.Dir(filepath.Dir(j.activeDir))
	checkpointDir := filepath.Join(mountDir, "checkpoints", id)

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
	// Get the mount directory from the active directory path
	mountDir := filepath.Dir(filepath.Dir(j.activeDir))
	checkpointDir := filepath.Join(mountDir, "checkpoints", id)

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

// Status returns the current status of the JuiceFS component
func (j *JuiceFSComponent) Status(ctx context.Context) map[string]interface{} {
	status := make(map[string]interface{})

	// Check if JuiceFS is mounted
	mountDir := filepath.Join(j.config.EnvDir, "juicefs")
	dbPath := filepath.Join(j.config.EnvDir, "db", "juicefs.sqlite")
	cmd := exec.CommandContext(ctx, "juicefs", "status", fmt.Sprintf("sqlite3://%s", dbPath), mountDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		status["mounted"] = false
		status["mount_error"] = err.Error()
	} else {
		status["mounted"] = true
		status["mount_status"] = string(output)
	}

	// Add DB manager status if available
	if j.dbManager != nil {
		status["db_manager"] = j.dbManager.Status(ctx)
	}

	return status
}
