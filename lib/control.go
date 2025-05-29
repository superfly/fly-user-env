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
	"strings"
	"sync"
	"time"
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

// DefaultSystemConfig returns a new SystemConfig with default values
func DefaultSystemConfig() SystemConfig {
	return SystemConfig{
		Storage: DefaultObjectStorageConfig(),
		Stacks:  []string{"leaser", "juicefs"},
	}
}

// DefaultObjectStorageConfig returns a new ObjectStorageConfig with default values
func DefaultObjectStorageConfig() ObjectStorageConfig {
	return ObjectStorageConfig{
		Region:    "auto",
		KeyPrefix: "/",
	}
}

// ControlHTTP represents a component that provides HTTP endpoints
type ControlHTTP interface {
	StackComponent
	// ServeHTTP handles HTTP requests for this component
	ServeHTTP(w http.ResponseWriter, r *http.Request)
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
	components     []StackComponent
	err            error
	mux            *http.ServeMux
}

// NewSystemConfigFromEnv creates a new SystemConfig from environment variables
func NewSystemConfigFromEnv() (*SystemConfig, error) {
	// Check for required storage environment variables
	bucket := os.Getenv("FLY_STORAGE_BUCKET")
	endpoint := os.Getenv("FLY_STORAGE_ENDPOINT")
	accessKey := os.Getenv("FLY_STORAGE_ACCESS_KEY")
	secretKey := os.Getenv("FLY_STORAGE_SECRET_KEY")

	// If any of the required storage variables are missing, return nil
	if bucket == "" || endpoint == "" || accessKey == "" || secretKey == "" {
		return nil, nil
	}

	// Start with default config
	cfg := DefaultSystemConfig()

	// Override with environment variables
	cfg.Storage.Bucket = bucket
	cfg.Storage.Endpoint = endpoint
	cfg.Storage.AccessKey = accessKey
	cfg.Storage.SecretKey = secretKey

	// Optional storage configuration
	if region := os.Getenv("FLY_STORAGE_REGION"); region != "" {
		cfg.Storage.Region = region
	}
	if keyPrefix := os.Getenv("FLY_STORAGE_KEY_PREFIX"); keyPrefix != "" {
		cfg.Storage.KeyPrefix = keyPrefix
	}

	// Get stacks from environment variable
	if stacks := os.Getenv("FLY_STACKS"); stacks != "" {
		cfg.Stacks = strings.Split(stacks, ",")
	}

	return &cfg, nil
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
		mux:            http.NewServeMux(),
	}

	// Set up initial routes (before config)
	c.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			c.handleStatus(w, r)
		} else if r.Method == http.MethodPost {
			c.handleConfig(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Check if we should wait for config
	waitForConfig := os.Getenv("FLY_ENV_WAIT_FOR_CONFIG") != ""

	// Try to load config from environment first
	if !waitForConfig {
		if envConfig, err := NewSystemConfigFromEnv(); err == nil && envConfig != nil {
			// Check if config file exists
			if _, err := os.Stat(configPath); err == nil {
				// Both environment variables and config file exist
				c.config = nil
				c.err = fmt.Errorf("configuration conflict: both environment variables and config file exist")
				return c
			}
			c.config = envConfig
			// Set up components with environment config
			if err := c.setupComponents(context.Background(), envConfig); err != nil {
				log.Printf("Failed to setup components from environment config: %v", err)
			}
			c.setupRoutes()
			return c
		}
	}

	// Try to load existing config file
	if err := c.loadConfig(); err != nil {
		log.Printf("No existing config found: %v", err)
	} else {
		c.setupRoutes()
	}

	return c
}

// setupRoutes configures the mux with routes based on the current configuration
func (c *Control) setupRoutes() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Create a new mux
	c.mux = http.NewServeMux()

	// Register component routes
	for _, comp := range c.components {
		if httpComp, ok := comp.(ControlHTTP); ok {
			name := getComponentName(comp)
			if name != "" {
				c.mux.Handle("/stack/"+name+"/", http.StripPrefix("/stack/"+name, httpComp))
			}
		}
	}

	// Register other routes
	c.mux.HandleFunc("/checkpoint", c.handleCheckpoint)
	c.mux.HandleFunc("/restore", c.handleRestore)
	c.mux.HandleFunc("/status", c.handleStatus)

	// Handle root path based on method
	c.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			c.handleStatus(w, r)
		} else if r.Method == http.MethodPost {
			c.handleConfig(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func (c *Control) handleConfig(w http.ResponseWriter, r *http.Request) {
	// Start with default config
	cfgData := DefaultSystemConfig()

	// Decode the request body into our config
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

	// Set up routes after components are configured
	c.setupRoutes()

	w.WriteHeader(http.StatusOK)
}

func (c *Control) handleStatus(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	status := struct {
		Configured bool     `json:"configured"`
		Running    bool     `json:"running"`
		Stacks     []string `json:"stacks"`
	}{
		Configured: c.config != nil,
		Running:    c.supervisor != nil && c.supervisor.IsRunning(),
		Stacks:     nil, // Will be empty slice when not configured
	}

	if status.Configured {
		status.Stacks = c.config.Stacks
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (c *Control) Status() interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()

	status := struct {
		Configured bool     `json:"configured"`
		Running    bool     `json:"running"`
		Stacks     []string `json:"stacks"`
	}{
		Configured: c.config != nil,
		Running:    c.supervisor != nil && c.supervisor.IsRunning(),
		Stacks:     nil, // Will be empty slice when not configured
	}

	if status.Configured {
		status.Stacks = c.config.Stacks
	}

	return status
}

func (c *Control) GetStorageConfig() *ObjectStorageConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return &c.config.Storage
}

func (c *Control) handleReleaseLease(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, comp := range c.components {
		if lc, ok := comp.(*LeaserComponent); ok {
			if err := lc.ReleaseAllLeases(r.Context()); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]string{"error": "No active leaser component"})
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

func (l *LeaserComponent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("LeaserComponent.ServeHTTP: path=%s, method=%s", r.URL.Path, r.Method)
	switch r.Method {
	case http.MethodPost:
		if r.URL.Path == "/release" {
			if err := l.ReleaseAllLeases(r.Context()); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "Not found", http.StatusNotFound)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// getComponentName returns the name of a component based on its type
func getComponentName(comp StackComponent) string {
	switch comp.(type) {
	case *DBManagerComponent:
		return "db"
	case *LeaserComponent:
		return "leaser"
	case *JuiceFSComponent:
		return "juicefs"
	default:
		return ""
	}
}

func (c *Control) setupComponents(ctx context.Context, cfg *SystemConfig) error {
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
	// Check for configuration error
	if c.err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": c.err.Error()})
		return
	}

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

	// Let the mux handle the request
	c.mux.ServeHTTP(w, r)
}
