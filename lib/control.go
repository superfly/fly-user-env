package lib

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	lss3 "github.com/benbjohnson/litestream/s3"
	// For types.NoSuchKey
)

// StackComponent represents a component in our stack that needs setup/cleanup
type StackComponent interface {
	// Setup initializes the component with the given config
	Setup(ctx context.Context, cfg *ObjectStorageConfig, juicefsPath string) error
	// Cleanup performs any necessary cleanup when the component is no longer needed
	Cleanup(ctx context.Context) error
	// Status returns the status of the component
	Status(ctx context.Context) map[string]interface{}
}

// CheckpointableComponent represents a component that supports checkpoint and restore operations
type CheckpointableComponent interface {
	StackComponent
	// CreateCheckpoint creates a checkpoint with the given ID and returns a checkpoint identifier
	CreateCheckpoint(ctx context.Context, id string) (string, error)
	// RestoreToCheckpoint restores the component to the state at the given checkpoint ID
	RestoreToCheckpoint(ctx context.Context, id string) error
}

// DBManagerComponent implements StackComponent and CheckpointableComponent
// rule: DBManagerComponent is not checkpointable for now, so these methods are no-ops

type DBManagerComponent struct {
	dbManager *DBManager
	dataDir   string
}

func NewDBManagerComponent(dataDir string) *DBManagerComponent {
	return &DBManagerComponent{dataDir: dataDir}
}

func (d *DBManagerComponent) Setup(ctx context.Context, cfg *ObjectStorageConfig, juicefsPath string) error {
	log.Printf("DBManagerComponent.Setup: dataDir=%s", d.dataDir)
	d.dbManager = NewDBManager(cfg, d.dataDir)
	log.Printf("DBManagerComponent.Setup: DBPath=%s", d.dbManager.DBPath)
	if err := d.dbManager.Initialize(); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	if err := d.dbManager.StartReplication(); err != nil {
		return fmt.Errorf("failed to start replication: %w", err)
	}
	return nil
}

func (d *DBManagerComponent) Cleanup(ctx context.Context) error {
	if d.dbManager != nil {
		return d.dbManager.StopReplication()
	}
	return nil
}

// No-op: DBManagerComponent is not checkpointable for now
func (d *DBManagerComponent) CreateCheckpoint(ctx context.Context, id string) (string, error) {
	return id, nil
}

// No-op: DBManagerComponent is not checkpointable for now
func (d *DBManagerComponent) RestoreToCheckpoint(ctx context.Context, id string) error {
	return nil
}

// abs returns the absolute value of a duration
func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// ObjectStorageConfig represents the configuration for object storage
type ObjectStorageConfig struct {
	Bucket    string `json:"bucket"`
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Region    string `json:"region"`
	KeyPrefix string `json:"key_prefix"`
	EnvDir    string `json:"env_dir"`
}

// SystemConfig represents the overall system configuration
type SystemConfig struct {
	Storage ObjectStorageConfig `json:"storage"`
	Stacks  []string            `json:"stacks"` // List of stack components to enable
}

// AdminConfig holds configuration for the admin interface.
type AdminConfig struct {
	// TimeoutStop is the time to wait for graceful shutdown before force killing.
	// Defaults to 90 seconds if not set (matching systemd's default).
	TimeoutStop time.Duration `yaml:"timeout_stop"`

	// RestartDelay is the time to wait before restarting a failed process.
	// Defaults to 100ms if not set (matching systemd's default).
	RestartDelay time.Duration `yaml:"restart_delay"`
}

// DefaultAdminConfig returns a new AdminConfig with default values.
func DefaultAdminConfig() AdminConfig {
	return AdminConfig{
		TimeoutStop:  90 * time.Second,
		RestartDelay: time.Second,
	}
}

// ControlConfig holds configuration for the control interface.
type ControlConfig struct {
	TargetAddr     string
	ControllerAddr string
	DataDir        string
	ConfigPath     string
	Token          string
}

// DefaultControlConfig returns a new ControlConfig with default values.
func DefaultControlConfig() ControlConfig {
	return ControlConfig{
		TargetAddr:     "localhost:8080",
		ControllerAddr: "localhost:8080",
		DataDir:        "tmp",
		ConfigPath:     "tmp/config.json",
		Token:          "test-token",
	}
}

// Control manages the control interface and object storage configuration
// and provides methods for configuring and monitoring the system.
type Control struct {
	mu             sync.RWMutex
	config         *SystemConfig
	configPath     string
	dataDir        string
	targetAddr     string
	controllerAddr string
	token          string
	supervisor     *Supervisor
	dbManager      *DBManager
	leaser         *lss3.Leaser
	components     []StackComponent
}

// NewControl creates a new control instance
func NewControl(targetAddr, controllerAddr, token, dataDir string, supervisor *Supervisor, components ...StackComponent) *Control {
	return NewControlWithConfig(targetAddr, controllerAddr, token, supervisor, filepath.Join(dataDir, "config.json"), dataDir, components...)
}

// NewControlWithConfig creates a new control instance with a custom config path
func NewControlWithConfig(targetAddr, controllerAddr, token string, supervisor *Supervisor, configPath, dataDir string, components ...StackComponent) *Control {
	c := &Control{
		targetAddr:     targetAddr,
		controllerAddr: controllerAddr,
		token:          token,
		configPath:     configPath,
		dataDir:        dataDir,
		supervisor:     supervisor,
		components:     components,
	}

	// Try to load existing config
	if err := c.loadConfig(); err != nil {
		log.Printf("No existing config found: %v", err)
	}

	return c
}

// getAvailableComponents returns a map of available components by name
func (c *Control) getAvailableComponents() map[string]StackComponent {
	components := make(map[string]StackComponent)
	for _, component := range c.components {
		switch comp := component.(type) {
		case *DBManagerComponent:
			components["db"] = comp
		case *LeaserComponent:
			components["leaser"] = comp
		case *JuiceFSComponent:
			components["juicefs"] = comp
		}
	}
	return components
}

func (c *Control) setupComponents(ctx context.Context, cfg *SystemConfig) error {
	// If no stacks specified, use all components
	if len(cfg.Stacks) == 0 {
		for _, component := range c.components {
			if err := component.Setup(ctx, &cfg.Storage, "juicefs"); err != nil {
				return fmt.Errorf("failed to setup component: %w", err)
			}
		}
		return nil
	}

	// Set up only the specified components
	for _, stackName := range cfg.Stacks {
		component, ok := c.getAvailableComponents()[stackName]
		if !ok {
			return fmt.Errorf("unknown stack component: %s", stackName)
		}
		log.Printf("Setting up component %s with dataDir: %s", stackName, c.dataDir)
		if err := component.Setup(ctx, &cfg.Storage, "juicefs"); err != nil {
			return fmt.Errorf("failed to setup component: %w", err)
		}
	}

	return nil
}

func (c *Control) loadConfig() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Read config file
	data, err := os.ReadFile(c.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse config
	var cfg SystemConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Store configs
	c.config = &cfg

	// Set up components
	if err := c.setupComponents(context.Background(), &cfg); err != nil {
		return fmt.Errorf("failed to set up components: %w", err)
	}

	return nil
}

func (c *Control) saveConfig() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.config == nil {
		return fmt.Errorf("no configuration to save")
	}

	// Create data directory if it doesn't exist
	if err := os.MkdirAll(c.dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Marshal config
	data, err := json.MarshalIndent(c.config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write config file
	if err := os.WriteFile(c.configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

func (c *Control) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != "fly-app-controller" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	auth := r.Header.Get("Authorization")
	expected := "Bearer " + c.token
	if auth != expected {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodPost:
		switch r.URL.Path {
		case "/release-lease":
			c.handleReleaseLease(w, r)
		case "/checkpoint":
			c.handleCheckpoint(w, r)
		case "/restore":
			c.handleRestore(w, r)
		default:
			c.handleConfig(w, r)
		}
	case http.MethodGet:
		c.handleStatus(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (c *Control) handleConfig(w http.ResponseWriter, r *http.Request) {
	var cfgData SystemConfig
	if err := json.NewDecoder(r.Body).Decode(&cfgData); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if cfgData.Storage.Bucket == "" || cfgData.Storage.Endpoint == "" ||
		cfgData.Storage.AccessKey == "" || cfgData.Storage.SecretKey == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	// Store the configurations
	c.config = &cfgData

	// Save config to file
	if err := c.saveConfig(); err != nil {
		http.Error(w, "Failed to save configuration", http.StatusInternalServerError)
		return
	}

	// Set up components
	if err := c.setupComponents(r.Context(), &cfgData); err != nil {
		http.Error(w, fmt.Sprintf("Failed to set up components: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (c *Control) handleStatus(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	status := struct {
		Configured bool `json:"configured"`
		Running    bool `json:"running"`
	}{
		Configured: c.config != nil,
		Running:    c.supervisor != nil && c.supervisor.IsRunning(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (c *Control) Status() interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return struct {
		Configured bool `json:"configured"`
		Running    bool `json:"running"`
	}{
		Configured: c.config != nil,
		Running:    c.supervisor != nil && c.supervisor.IsRunning(),
	}
}

func (c *Control) GetStorageConfig() *ObjectStorageConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return &c.config.Storage
}

func (c *Control) GetLeaser() *lss3.Leaser {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.leaser
}

func (c *Control) handleReleaseLease(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var leaserComponent *LeaserComponent
	for _, comp := range c.components {
		if lc, ok := comp.(*LeaserComponent); ok {
			leaserComponent = lc
			break
		}
	}
	if leaserComponent == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "No active leaser component"})
		return
	}
	if err := leaserComponent.ReleaseAllLeases(r.Context()); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (c *Control) configureS3Leaser(ctx context.Context, config *ObjectStorageConfig) error {
	leaser := lss3.NewLeaser()
	leaser.Bucket = config.Bucket
	leaser.Endpoint = config.Endpoint
	leaser.AccessKeyID = config.AccessKey
	leaser.SecretAccessKey = config.SecretKey
	leaser.Region = config.Region
	leaser.ForcePathStyle = true
	leaser.Path = "leases/fly.lock"
	leaser.Owner = os.Getenv("HOSTNAME") // Use hostname as lease owner
	leaser.LeaseTimeout = 30 * time.Second

	if err := leaser.Open(); err != nil {
		return fmt.Errorf("failed to open leaser: %w", err)
	}

	c.leaser = leaser

	// Start the process
	if err := c.supervisor.StartProcess(); err != nil {
		return fmt.Errorf("failed to start process: %v", err)
	}

	return nil
}

// handleCheckpoint creates checkpoints for all checkpointable components and returns their status
func (c *Control) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.config == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "Not configured",
			"stack": string(debug.Stack()),
		})
		return
	}

	var req struct {
		CheckpointID string `json:"checkpoint_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Invalid request body: %v", err)})
		return
	}

	if req.CheckpointID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Checkpoint ID is required"})
		return
	}

	checkpointables := []CheckpointableComponent{}
	for _, comp := range c.components {
		if cc, ok := comp.(CheckpointableComponent); ok {
			checkpointables = append(checkpointables, cc)
		}
	}
	if len(checkpointables) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "No checkpointable components available"})
		return
	}

	results := make(map[string]string)
	for _, cc := range checkpointables {
		id, err := cc.CreateCheckpoint(r.Context(), req.CheckpointID)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		results[fmt.Sprintf("%T", cc)] = id
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        "success",
		"checkpoint_id": req.CheckpointID,
		"results":       results,
	})
}

// handleRestore restores all checkpointable components to the specified checkpoint
func (c *Control) handleRestore(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.config == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Not configured"})
		return
	}

	var req struct {
		CheckpointID string `json:"checkpoint_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Invalid request body: %v", err)})
		return
	}

	if req.CheckpointID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Checkpoint ID is required"})
		return
	}

	checkpointables := []CheckpointableComponent{}
	for _, comp := range c.components {
		if cc, ok := comp.(CheckpointableComponent); ok {
			checkpointables = append(checkpointables, cc)
		}
	}
	if len(checkpointables) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "No checkpointable components available"})
		return
	}

	for _, cc := range checkpointables {
		if err := cc.RestoreToCheckpoint(r.Context(), req.CheckpointID); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        "success",
		"checkpoint_id": req.CheckpointID,
	})
}

// Cleanup performs cleanup of all components
func (c *Control) Cleanup(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Clean up components in reverse order
	for i := len(c.components) - 1; i >= 0; i-- {
		component := c.components[i]
		if err := component.Cleanup(ctx); err != nil {
			return fmt.Errorf("failed to cleanup component: %w", err)
		}
	}
	return nil
}

// Shutdown gracefully shuts down the control server
func (c *Control) Shutdown(ctx context.Context) error {
	// First cleanup all components
	if err := c.Cleanup(ctx); err != nil {
		return fmt.Errorf("failed to cleanup components: %w", err)
	}

	// Then stop the supervisor if it exists
	if c.supervisor != nil {
		if err := c.supervisor.StopProcess(); err != nil {
			return fmt.Errorf("failed to stop supervisor: %w", err)
		}
	}

	return nil
}
