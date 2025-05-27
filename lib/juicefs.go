package lib

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// JuiceFSComponent implements StackComponent and CheckpointableComponent for JuiceFS file system management
type JuiceFSComponent struct {
	config    *ObjectStorageConfig
	mountDir  string // Store the mount directory explicitly
	activeDir string
	dbManager *DBManager
	mountCmd  *exec.Cmd
	mountCtx  context.Context
}

// NewJuiceFSComponent creates a new JuiceFS component
func NewJuiceFSComponent() *JuiceFSComponent {
	return &JuiceFSComponent{
		mountCtx: context.Background(),
	}
}

// SetMountContext sets the context to use for the mount process
func (j *JuiceFSComponent) SetMountContext(ctx context.Context) {
	j.mountCtx = ctx
}

// Setup initializes the JuiceFS component with the given config
func (j *JuiceFSComponent) Setup(ctx context.Context, cfg *ObjectStorageConfig, juicefsPath string) error {
	j.config = cfg

	// Create mount directory (absolute path)
	mountDir, err := filepath.Abs(filepath.Join(cfg.EnvDir, "juicefs"))
	if err != nil {
		return fmt.Errorf("failed to get absolute path for mount directory: %w", err)
	}
	if err := os.MkdirAll(mountDir, 0755); err != nil {
		return fmt.Errorf("failed to create mount directory: %w", err)
	}
	j.mountDir = mountDir // Store the mount directory

	// Create db directory (absolute path) - separate from mount
	dbDir, err := filepath.Abs(filepath.Join(cfg.EnvDir, "db"))
	if err != nil {
		return fmt.Errorf("failed to get absolute path for db directory: %w", err)
	}
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("failed to create db directory: %w", err)
	}

	// Initialize SQLite database for metadata (absolute path) - separate from mount
	dbPath := filepath.Join(dbDir, "juicefs.sqlite")
	j.dbManager = NewDBManager(cfg, dbDir)
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

	// Log the mount command
	log.Printf("Running mount command: %s", formatCmd.String())
	mountCmd := exec.CommandContext(j.mountCtx, juicefsPath, "mount",
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

	mountCmd.Stdout = os.Stdout
	mountCmd.Stderr = os.Stderr
	if err := mountCmd.Start(); err != nil {
		log.Printf("Mount command failed to start: %v", err)
		return fmt.Errorf("failed to start JuiceFS mount: %w", err)
	}
	j.mountCmd = mountCmd
	go func() {
		if err := mountCmd.Wait(); err != nil {
			fmt.Printf("Mount process exited with error: %v\n", err)
		} else {
			fmt.Println("Mount process exited successfully.")
		}
	}()

	// Give it a moment to start
	time.Sleep(2 * time.Second)

	// Verify it's running
	fmt.Println("Running juicefs status check...")
	statusCmd := exec.CommandContext(ctx, "juicefs", "status", fmt.Sprintf("sqlite3://%s", dbPath), mountDir)
	statusOutput, err := statusCmd.CombinedOutput()
	fmt.Printf("JuiceFS status command completed with err=%v\n", err)
	if err != nil {
		return fmt.Errorf("failed to verify JuiceFS mount: %w\nOutput: %s", err, string(statusOutput))
	}
	fmt.Printf("JuiceFS mount status: %s\n", string(statusOutput))

	// Create active and checkpoints directories within the mount
	activeDir := filepath.Join(j.mountDir, "active")
	checkpointsDir := filepath.Join(j.mountDir, "checkpoints")
	if err := os.MkdirAll(activeDir, 0755); err != nil {
		return fmt.Errorf("failed to create active directory: %w", err)
	}
	if err := os.MkdirAll(checkpointsDir, 0755); err != nil {
		return fmt.Errorf("failed to create checkpoints directory: %w", err)
	}

	// Set active directory path within the mount
	j.activeDir = activeDir

	// Log the state of the active directory and mount process before checkpointing
	log.Printf("Checking active directory at %s", j.activeDir)
	if _, err := os.Stat(j.activeDir); os.IsNotExist(err) {
		log.Printf("Active directory does not exist at %s", j.activeDir)
	} else {
		log.Printf("Active directory exists at %s", j.activeDir)
	}

	// Check mount process status
	log.Printf("Checking mount process status...")

	// Log the state of the active and checkpoints directories after creation
	log.Printf("Active directory created at %s", activeDir)
	log.Printf("Checkpoints directory created at %s", checkpointsDir)

	return nil
}

// Cleanup performs cleanup when the component is no longer needed
func (j *JuiceFSComponent) Cleanup(ctx context.Context) error {
	if j.dbManager != nil {
		if err := j.dbManager.StopReplication(); err != nil {
			return fmt.Errorf("failed to stop replication: %w", err)
		}
	}
	if j.mountCmd != nil && j.mountCmd.Process != nil {
		// Send SIGTERM to the mount process
		if err := j.mountCmd.Process.Signal(os.Interrupt); err != nil {
			// If SIGTERM fails, try SIGKILL
			if err := j.mountCmd.Process.Kill(); err != nil {
				return fmt.Errorf("failed to kill mount process: %w", err)
			}
		}
		// Wait for the process to exit
		if err := j.mountCmd.Wait(); err != nil {
			fmt.Printf("Mount process exited with error during cleanup: %v\n", err)
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

	// Use the stored mount directory
	checkpointDir := filepath.Join(j.mountDir, "checkpoints", id)

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
	// Use the stored mount directory
	checkpointDir := filepath.Join(j.mountDir, "checkpoints", id)

	// Remove current active
	if err := os.RemoveAll(j.activeDir); err != nil {
		return fmt.Errorf("failed to remove active directory: %w", err)
	}

	// Move checkpoint to active
	if err := os.Rename(checkpointDir, j.activeDir); err != nil {
		return fmt.Errorf("failed to move checkpoint to active: %w", err)
	}

	return nil
}

// Status returns the current status of the JuiceFS component
func (j *JuiceFSComponent) Status(ctx context.Context) map[string]interface{} {
	status := make(map[string]interface{})

	// Use the stored mount directory
	dbPath := filepath.Join(j.config.EnvDir, "db", "juicefs.sqlite")
	cmd := exec.CommandContext(ctx, "juicefs", "status", fmt.Sprintf("sqlite3://%s", dbPath), j.mountDir)
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
