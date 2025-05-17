package lib

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ObjectStorageConfig represents the configuration for object storage
type ObjectStorageConfig struct {
	Bucket    string `json:"bucket"`
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Region    string `json:"region"`
	KeyPrefix string `json:"key_prefix"`
}

// Admin manages the admin interface and object storage configuration
type Admin struct {
	mu            sync.RWMutex
	storageConfig *ObjectStorageConfig
	supervisor    *Supervisor
	token         string
	dbManager     *DBManager

	// Lock state
	lockETag   string
	lockCancel context.CancelFunc
}

// NewAdmin creates a new admin instance
func NewAdmin(supervisor *Supervisor, token string) *Admin {
	return &Admin{
		supervisor: supervisor,
		token:      token,
	}
}

// ServeHTTP handles HTTP requests for the control interface
func (c *Admin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		c.handleConfig(w, r)
	case http.MethodGet:
		c.handleStatus(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleConfig processes POST requests to configure object storage
func (c *Admin) handleConfig(w http.ResponseWriter, r *http.Request) {
	var config ObjectStorageConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if config.Bucket == "" || config.AccessKey == "" || config.SecretKey == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	// Store the configuration
	c.mu.Lock()
	c.storageConfig = &config
	c.mu.Unlock()

	// --- ENVIRONMENT LOCKING ---
	ctx, cancel := context.WithCancel(context.Background())
	c.lockCancel = cancel
	log.Printf("[Tigris S3 Config] Bucket: %s, Endpoint: %s, KeyPrefix: %s", config.Bucket, config.Endpoint, config.KeyPrefix)

	client := s3.New(s3.Options{
		Region:       config.Region,
		BaseEndpoint: aws.String(config.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(config.AccessKey, config.SecretKey, ""),
	})

	lockKey := config.KeyPrefix + "fly.lock"
	lockBody := strings.NewReader("locked")
	expiration := time.Now().Add(5 * time.Minute)

	// Try to acquire lock (create if not exists, If-Match: "")
	putOut, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:  aws.String(config.Bucket),
		Key:     aws.String(lockKey),
		Body:    lockBody,
		Expires: aws.Time(expiration),
		IfMatch: aws.String(""), // Only succeed if file does not exist
	})
	if err != nil {
		log.Printf("Failed to create lock: %v", err)
		http.Error(w, "Failed to acquire environment lock (already locked?)", http.StatusConflict)
		return
	}
	c.lockETag = aws.ToString(putOut.ETag)
	log.Printf("Lock acquired for bucket %s, expires at %s, ETag: %s", config.Bucket, expiration.Format(time.RFC3339), c.lockETag)

	// Start lock refresh goroutine
	go c.refreshLock(ctx, client, config.Bucket, lockKey)

	// --- DISABLE DB MANAGER SETUP ---
	// c.dbManager = NewDBManager(&config)
	// if err := c.dbManager.Initialize(); err != nil {
	// 	http.Error(w, fmt.Sprintf("Failed to initialize database: %v", err), http.StatusInternalServerError)
	// 	return
	// }

	// --- DISABLE SUPERVISOR START IF LOCK NOT ACQUIRED ---
	if err := c.supervisor.StartProcess(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to start process: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// refreshLock periodically refreshes the lock file using If-Match and the stored ETag
func (c *Admin) refreshLock(ctx context.Context, client *s3.Client, bucket, lockKey string) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			c.releaseLock(context.Background(), client, bucket, lockKey)
			return
		case <-ticker.C:
			c.mu.RLock()
			etag := c.lockETag
			c.mu.RUnlock()
			if etag == "" {
				continue
			}
			lockBody := strings.NewReader("locked")
			expiration := time.Now().Add(5 * time.Minute)
			putOut, err := client.PutObject(ctx, &s3.PutObjectInput{
				Bucket:  aws.String(bucket),
				Key:     aws.String(lockKey),
				Body:    lockBody,
				Expires: aws.Time(expiration),
				IfMatch: aws.String(etag),
			})
			if err != nil {
				log.Printf("Failed to refresh lock: %v", err)
				continue
			}
			c.mu.Lock()
			c.lockETag = aws.ToString(putOut.ETag)
			c.mu.Unlock()
			log.Printf("Lock refreshed, new ETag: %s, expires at %s", c.lockETag, expiration.Format(time.RFC3339))
		}
	}
}

// releaseLock deletes the lock file on shutdown
func (c *Admin) releaseLock(ctx context.Context, client *s3.Client, bucket, lockKey string) {
	_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(lockKey),
	})
	if err != nil {
		log.Printf("Failed to delete lock: %v", err)
	} else {
		log.Printf("Lock deleted for bucket %s", bucket)
	}
}

// handleStatus returns the current status of the controller
func (c *Admin) handleStatus(w http.ResponseWriter, r *http.Request) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	status := struct {
		Configured bool `json:"configured"`
		Running    bool `json:"running"`
	}{
		Configured: c.storageConfig != nil,
		Running:    c.supervisor.IsRunning(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// GetStorageConfig returns the current object storage configuration
func (c *Admin) GetStorageConfig() *ObjectStorageConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.storageConfig
}
