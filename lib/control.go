package lib

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	lss3 "github.com/benbjohnson/litestream/s3"
	// For types.NoSuchKey
)

// StackComponent represents a component in our stack that needs setup/cleanup
type StackComponent interface {
	// Setup initializes the component with the given config
	Setup(ctx context.Context, cfg *ObjectStorageConfig) error
	// Cleanup performs any necessary cleanup when the component is no longer needed
	Cleanup(ctx context.Context) error
}

// DBManagerComponent implements StackComponent for SQLite database management
type DBManagerComponent struct {
	dbManager *DBManager
}

func NewDBManagerComponent() *DBManagerComponent {
	return &DBManagerComponent{}
}

func (d *DBManagerComponent) Setup(ctx context.Context, cfg *ObjectStorageConfig) error {
	d.dbManager = NewDBManager(cfg)
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

// ObjectStorageConfig represents the configuration for object storage
type ObjectStorageConfig struct {
	Bucket    string `json:"bucket"`
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Region    string `json:"region"`
	KeyPrefix string `json:"key_prefix"`
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

// Admin manages the admin interface and object storage configuration
// NEVER use env vars or default credentials, wait for config to be posted
// rule: Only configure S3 after config POST
type Admin struct {
	mu             sync.Mutex
	s3cfg          aws.Config
	s3Config       *ObjectStorageConfig
	targetAddr     string
	controllerAddr string
	supervisor     *Supervisor
	components     []StackComponent
}

// NewAdmin creates a new admin instance
func NewAdmin(targetAddr, controllerAddr string, supervisor *Supervisor, components ...StackComponent) *Admin {
	// Default to leaser + DB manager if no components provided
	if len(components) == 0 {
		components = []StackComponent{
			NewLeaserComponent(),
			NewDBManagerComponent(),
		}
	}

	a := &Admin{
		targetAddr:     targetAddr,
		controllerAddr: controllerAddr,
		supervisor:     supervisor,
		components:     components,
	}
	// NEVER use env vars or default credentials, wait for config to be posted
	// S3 config/client will be created after config POST
	return a
}

// ServeHTTP handles HTTP requests for the control interface
func (c *Admin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != "fly-app-controller" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	auth := r.Header.Get("Authorization")
	expected := "Bearer " + c.controllerAddr
	if auth != expected {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodPost:
		if r.URL.Path == "/release-lease" {
			c.handleReleaseLease(w, r)
		} else {
			c.handleConfig(w, r)
		}
	case http.MethodGet:
		c.handleStatus(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleConfig processes POST requests to configure object storage
func (c *Admin) handleConfig(w http.ResponseWriter, r *http.Request) {
	var cfgData ObjectStorageConfig
	if err := json.NewDecoder(r.Body).Decode(&cfgData); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if cfgData.Bucket == "" || cfgData.AccessKey == "" || cfgData.SecretKey == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	// Store the configuration first
	c.s3Config = &cfgData

	// Create AWS config and S3 client using the posted config
	customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		return aws.Endpoint{
			URL: cfgData.Endpoint,
		}, nil
	})
	awsCfg, err := config.LoadDefaultConfig(r.Context(),
		config.WithEndpointResolverWithOptions(customResolver),
		config.WithRegion(cfgData.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfgData.AccessKey,
			cfgData.SecretKey,
			"",
		)),
	)
	if err != nil {
		http.Error(w, "Failed to create AWS config: "+err.Error(), http.StatusBadRequest)
		return
	}
	c.s3cfg = awsCfg

	// Set up each component in sequence
	for _, component := range c.components {
		if err := component.Setup(r.Context(), &cfgData); err != nil {
			http.Error(w, fmt.Sprintf("Failed to setup component: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Start the process
	if err := c.supervisor.StartProcess(); err != nil {
		log.Printf("Failed to start process: %v", err)
		http.Error(w, "Failed to start process", http.StatusInternalServerError)
		return
	}

	// Return success
	w.WriteHeader(http.StatusOK)
}

// handleStatus returns the current status of the controller
func (c *Admin) handleStatus(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	status := struct {
		Configured bool `json:"configured"`
		Running    bool `json:"running"`
	}{
		Configured: c.s3Config != nil,
		Running:    c.supervisor != nil && c.supervisor.IsRunning(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// Status returns the current status of the controller (intended for internal use perhaps, or if handleStatus is removed)
// If this is a duplicate or unused, it might need to be removed. For now, fixing mu.
func (c *Admin) Status() interface{} { // This method was added in a previous problematic diff, ensuring its mu is correct.
	c.mu.Lock()
	defer c.mu.Unlock()
	return struct {
		Configured bool `json:"configured"`
		Running    bool `json:"running"`
	}{
		Configured: c.s3Config != nil,
		Running:    c.supervisor != nil && c.supervisor.IsRunning(),
	}
}

// GetStorageConfig returns the current object storage configuration
// This method needs to be thread-safe
func (c *Admin) GetStorageConfig() *ObjectStorageConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.s3Config
}

// GetLeaser returns the current leaser instance (for testing only)
func (c *Admin) GetLeaser() *lss3.Leaser {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.components[0].(*LeaserComponent).leaser
}

// handleReleaseLease releases the current lease
func (c *Admin) handleReleaseLease(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	leaser := c.GetLeaser()
	if leaser == nil {
		http.Error(w, "No active lease", http.StatusNotFound)
		return
	}

	// Get all epochs to find active leases
	epochs, err := leaser.Epochs(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list epochs: %v", err), http.StatusInternalServerError)
		return
	}

	// Release each lease
	for _, epoch := range epochs {
		if err := leaser.ReleaseLease(r.Context(), epoch); err != nil {
			http.Error(w, fmt.Sprintf("Failed to release lease %d: %v", epoch, err), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
}

// Configure S3 leaser for distributed locking
func (c *Admin) configureS3Leaser(ctx context.Context, config *ObjectStorageConfig) error {
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

	c.components[0] = &LeaserComponent{leaser: leaser, owner: os.Getenv("HOSTNAME")}

	// Start the process
	if err := c.supervisor.StartProcess(); err != nil {
		return fmt.Errorf("failed to start process: %v", err)
	}

	return nil
}
