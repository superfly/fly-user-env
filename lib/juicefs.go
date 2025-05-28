package lib

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// JuiceFSComponent implements StackComponent and CheckpointableComponent for JuiceFS file system management
type JuiceFSComponent struct {
	config            *ObjectStorageConfig
	basePath          string // Absolute base path for all JuiceFS operations
	activeDir         string
	dbManager         *DBManager
	supervisor        *Supervisor
	isReady           bool
	shutdownRequested bool
	mu                sync.RWMutex // protect isReady, mountCmd, and shutdownRequested access
	stderrReader      io.ReadCloser
}

// NewJuiceFSComponent creates a new JuiceFS component
func NewJuiceFSComponent() *JuiceFSComponent {
	return &JuiceFSComponent{}
}

// SetMountContext sets the context to use for the mount process
func (j *JuiceFSComponent) SetMountContext(ctx context.Context) {
	// The supervisor handles the mount process, so no need to set mountCtx
}

// Setup initializes the JuiceFS component with the given config
func (j *JuiceFSComponent) Setup(ctx context.Context, cfg *ObjectStorageConfig, juicefsPath string) error {
	j.config = cfg

	// Convert base path to absolute path
	basePath, err := filepath.Abs(cfg.EnvDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for base directory: %w", err)
	}
	j.basePath = basePath

	// Create mount directory
	mountDirStart := time.Now()
	mountDir := filepath.Join(j.basePath, "juicefs")
	if err := os.MkdirAll(mountDir, 0755); err != nil {
		return fmt.Errorf("failed to create mount directory: %w", err)
	}
	log.Printf("Mount directory setup took %v", time.Since(mountDirStart))

	// Create db directory - separate from mount
	dbDirStart := time.Now()
	dbDir := filepath.Join(j.basePath, "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("failed to create db directory: %w", err)
	}
	log.Printf("DB directory setup took %v", time.Since(dbDirStart))

	// Initialize SQLite database for metadata
	dbInitStart := time.Now()
	dbPath := filepath.Join(dbDir, "juicefs.sqlite")
	j.dbManager = NewDBManager(cfg, dbDir)
	j.dbManager.DBPath = dbPath
	if err := j.dbManager.Initialize(); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	if err := j.dbManager.StartReplication(); err != nil {
		return fmt.Errorf("failed to start replication: %w", err)
	}
	log.Printf("DB initialization and replication start took %v", time.Since(dbInitStart))

	// Format the filesystem if it doesn't exist
	formatStart := time.Now()
	formatCmd := exec.CommandContext(ctx, juicefsPath, "format",
		"--storage", "s3",
		"--bucket", cfg.Endpoint+"/"+cfg.Bucket,
		"--trash-days", "0",
		fmt.Sprintf("sqlite3://%s", dbPath),
		"juicefs")

	// Set environment variables for authentication during format
	formatCmd.Env = append(os.Environ(),
		"AWS_ACCESS_KEY_ID="+cfg.AccessKey,
		"AWS_SECRET_ACCESS_KEY="+cfg.SecretKey,
		"AWS_ENDPOINT_URL="+cfg.Endpoint,
		"AWS_REGION="+cfg.Region,
	)

	// Capture format command output
	formatOutput, err := formatCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to format JuiceFS: %w\nOutput: %s", err, string(formatOutput))
	}
	fmt.Printf("JuiceFS format output: %s\n", string(formatOutput))
	log.Printf("JuiceFS format took %v", time.Since(formatStart))

	// Start the mount process
	mountStart := time.Now()

	// Create mount command
	mountCmd := exec.Command(juicefsPath, "mount", "--no-syslog", "--no-color",
		fmt.Sprintf("sqlite3://%s", dbPath), mountDir)
	mountCmd.Env = append(os.Environ(),
		"AWS_ACCESS_KEY_ID="+cfg.AccessKey,
		"AWS_SECRET_ACCESS_KEY="+cfg.SecretKey,
		"AWS_ENDPOINT_URL="+cfg.Endpoint,
		"AWS_REGION="+cfg.Region,
	)

	// Set up stdout/stderr before creating supervisor
	mountCmd.Stdout = os.Stdout
	stderr, err := mountCmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}
	j.stderrReader = stderr

	// Create supervisor for mount process
	j.supervisor = NewSupervisorCmd(mountCmd, SupervisorConfig{
		TimeoutStop: 90 * time.Second,
	})

	// Start the supervisor
	if err := j.supervisor.StartProcess(); err != nil {
		return fmt.Errorf("failed to start juicefs mount: %v", err)
	}

	// Create a channel to signal when the mount is ready
	mountReady := make(chan error, 1)

	// Monitor stderr for the ready message
	go func() {
		defer close(mountReady)
		scanner := bufio.NewScanner(j.stderrReader)
		expectedPath := mountDir
		readyMsg := fmt.Sprintf("juicefs is ready at %s", expectedPath)
		log.Printf("Waiting for ready message: %q", readyMsg)
		for scanner.Scan() {
			line := scanner.Text()
			log.Printf("juicefs mount stderr: %s", line)
			if strings.Contains(line, readyMsg) {
				log.Printf("juicefs mount ready message detected")
				mountReady <- nil
				return
			} else {
				// Print raw text when no match
				log.Printf("juicefs mount stderr (raw): %q", line)
			}
		}
		if err := scanner.Err(); err != nil {
			mountReady <- fmt.Errorf("error reading mount stderr: %v", err)
		}
	}()

	// Wait for mount to be ready or timeout
	select {
	case err := <-mountReady:
		if err != nil {
			j.mu.Lock()
			if j.supervisor != nil {
				j.supervisor.StopProcess()
			}
			j.mu.Unlock()
			return fmt.Errorf("mount failed: %v", err)
		}
	case <-time.After(60 * time.Second):
		j.mu.Lock()
		if j.supervisor != nil {
			j.supervisor.StopProcess()
		}
		j.mu.Unlock()
		return fmt.Errorf("mount timed out after 60 seconds")
	case <-ctx.Done():
		j.mu.Lock()
		if j.supervisor != nil {
			j.supervisor.StopProcess()
		}
		j.mu.Unlock()
		return ctx.Err()
	}

	// Mount is ready
	j.mu.Lock()
	j.isReady = true
	j.mu.Unlock()

	log.Printf("JuiceFS mount took %v", time.Since(mountStart))

	// Create active and checkpoints directories within the mount
	dirsStart := time.Now()
	activeDir := filepath.Join(mountDir, "active")
	checkpointsDir := filepath.Join(mountDir, "checkpoints")
	if err := os.MkdirAll(activeDir, 0755); err != nil {
		return fmt.Errorf("failed to create active directory: %w", err)
	}
	if err := os.MkdirAll(checkpointsDir, 0755); err != nil {
		return fmt.Errorf("failed to create checkpoints directory: %w", err)
	}
	log.Printf("Creating active and checkpoints directories took %v", time.Since(dirsStart))

	// Set active directory path within the mount
	j.activeDir = activeDir

	// Log the state of the active directory and mount process before checkpointing
	log.Printf("Checking active directory at %s", j.activeDir)
	if _, err := os.Stat(j.activeDir); os.IsNotExist(err) {
		log.Printf("Active directory does not exist at %s", j.activeDir)
	} else {
		log.Printf("Active directory exists at %s", j.activeDir)
	}

	return nil
}

// Status returns the current status of the component
func (j *JuiceFSComponent) Status(ctx context.Context) map[string]interface{} {
	j.mu.RLock()
	defer j.mu.RUnlock()

	status := make(map[string]interface{})
	status["ready"] = j.isReady
	status["process_running"] = j.supervisor != nil
	return status
}

// Cleanup performs cleanup when the component is no longer needed
func (j *JuiceFSComponent) Cleanup(ctx context.Context) error {
	j.mu.Lock()
	j.shutdownRequested = true
	j.mu.Unlock()

	if j.supervisor != nil {
		if err := j.supervisor.StopProcess(); err != nil {
			log.Printf("Failed to stop mount process: %v", err)
		}
	}

	// Clean up DB manager if it exists
	if j.dbManager != nil {
		if err := j.dbManager.StopReplication(); err != nil {
			log.Printf("Failed to stop DB replication: %v", err)
		}
	}

	return nil
}

// Shutdown performs a graceful shutdown of the component
func (j *JuiceFSComponent) Shutdown(ctx context.Context) error {
	return j.Cleanup(ctx)
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

	// Use the base path for checkpoint directory
	checkpointDir := filepath.Join(j.basePath, "juicefs", "checkpoints", id)

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
	// Use the base path for checkpoint directory
	checkpointDir := filepath.Join(j.basePath, "juicefs", "checkpoints", id)

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
