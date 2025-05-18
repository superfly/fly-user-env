package lib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types" // For types.NoSuchKey
)

const (
	// rule: Lock filename is fly.lock
	lockFile            = "fly.lock"
	lockTTL             = 5 * time.Minute // How long a lock is valid for once acquired
	lockAcquireTimeout  = 5 * time.Minute // How long we attempt to acquire a lock
	lockAcquireInterval = 5 * time.Second
)

// LockInfo holds the information stored in the lock file.
type LockInfo struct {
	Hostname  string    `json:"hostname"`
	PID       int       `json:"pid"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Format converts LockInfo to a string representation for storing in the lock file.
// rule: Lock content is "hostname:pid:expires_at_unix_ts".
func (li *LockInfo) Format() string {
	// Always use UTC time to avoid timezone issues
	return fmt.Sprintf("%s:%d:%d", li.Hostname, li.PID, li.ExpiresAt.UTC().Unix())
}

// ParseLockInfo parses the string data from the lock file into LockInfo.
func ParseLockInfo(data string) (*LockInfo, error) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid lock data format: expected 3 parts, got %d ('%s')", len(parts), data)
	}

	pid, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid PID in lock data '%s': %w", parts[1], err)
	}

	expiresUnix, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid expiry timestamp in lock data '%s': %w", parts[2], err)
	}

	// Always use UTC time to avoid timezone issues
	expiresAt := time.Unix(expiresUnix, 0).UTC()

	return &LockInfo{
		Hostname:  parts[0],
		PID:       pid,
		ExpiresAt: expiresAt,
	}, nil
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

// Admin manages the admin interface and object storage configuration
// NEVER use env vars or default credentials, wait for config to be posted
// rule: Only configure S3 after config POST
type Admin struct {
	mu             sync.Mutex
	s3cfg          aws.Config
	s3Config       *ObjectStorageConfig
	dbManager      *DBManager
	targetAddr     string
	controllerAddr string
	lockAcquired   bool
	lockOwner      string // Store lock owner info for error messages
	lockWG         sync.WaitGroup
	s3Client       interface {
		GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
		PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
		DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	}
	lockTTL             time.Duration
	lockAcquireTimeout  time.Duration
	lockAcquireInterval time.Duration
}

// NewAdmin creates a new admin instance
func NewAdmin(targetAddr, controllerAddr string) *Admin {
	a := &Admin{
		targetAddr:          targetAddr,
		controllerAddr:      controllerAddr,
		dbManager:           NewDBManager(nil), // Initialize with nil config, will be set later
		lockTTL:             lockTTL,
		lockAcquireTimeout:  lockAcquireTimeout,
		lockAcquireInterval: lockAcquireInterval,
	}
	// NEVER use env vars or default credentials, wait for config to be posted
	// S3 config/client will be created after config POST
	return a
}

// SetS3Client sets a custom S3 client for testing
func (c *Admin) SetS3Client(client interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.s3Client = client
}

// SetLockTimeouts sets custom timeouts for testing
func (c *Admin) SetLockTimeouts(ttl, acquireTimeout time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lockTTL = ttl
	c.lockAcquireTimeout = acquireTimeout
	c.lockAcquireInterval = acquireTimeout / 10 // Keep the ratio consistent
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
		c.handleConfig(w, r)
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
	c.s3Client = s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})

	// Try to acquire the S3 lock
	// rule: Acquire lock before initializing DB manager and starting replication
	if err := c.acquireLock(r.Context(), true); err != nil {
		log.Printf("Failed to acquire environment lock: %v", err)
		http.Error(w, fmt.Sprintf("Failed to acquire environment lock: %v", err), http.StatusConflict) // 409 Conflict
		return
	}
	// rule: Defer lock release to ensure it's cleaned up
	c.lockWG.Add(1) // Assuming lockWG is for tracking active critical sections like this one
	defer func() {
		c.releaseLock(r.Context()) // Pass context for cancellation
		c.lockWG.Done()
	}()

	// Initialize DB manager with the new config
	c.dbManager = NewDBManager(&cfgData)

	// Initialize DB manager (restores from backup if necessary)
	if err := c.dbManager.Initialize(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to initialize database: %v", err), http.StatusInternalServerError)
		return
	}

	// Start database replication
	if err := c.dbManager.StartReplication(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to start database replication: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// acquireLock attempts to acquire a distributed lock using S3.
// It writes a lock file containing hostname, PID, and expiry.
// It will attempt to overwrite an existing lock if it's expired or unparseable.
// Returns an error if lock acquisition fails or if retryOnLockHeld is false and lock is held.
func (c *Admin) acquireLock(ctx context.Context, retryOnLockHeld bool) error {
	// rule: The lock TTL should be configurable, for now, 5 minutes.
	// rule: Lock content is "hostname:pid:expires_at_unix_ts".
	// rule: If lock exists, read content. If expired, try to overwrite.
	// rule: Use S3 ETag for conditional overwrite of expired/unparseable locks.

	currentHostname, err := os.Hostname()
	if err != nil {
		// Fallback hostname if os.Hostname() fails
		currentHostname = "unknown-host"
		log.Printf("Warning: could not get hostname for lock: %v", err)
	}
	currentPID := os.Getpid()

	var s3Client interface {
		GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
		PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
		DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	}

	c.mu.Lock()
	if c.s3Client != nil {
		s3Client = c.s3Client
	} else {
		s3Client = s3.NewFromConfig(c.s3cfg, func(o *s3.Options) {
			o.UsePathStyle = true
		})
	}
	lockTTL := c.lockTTL
	lockAcquireTimeout := c.lockAcquireTimeout
	lockAcquireInterval := c.lockAcquireInterval
	c.mu.Unlock()

	overallTimeoutCtx, cancelOverallTimeout := context.WithTimeout(ctx, lockAcquireTimeout)
	defer cancelOverallTimeout()

	attempt := 0
	for {
		attempt++
		select {
		case <-overallTimeoutCtx.Done():
			log.Printf("Failed to acquire lock: timeout after %v", lockAcquireTimeout)
			return fmt.Errorf("timed out acquiring lock after %v: %w", lockAcquireTimeout, overallTimeoutCtx.Err())
		default:
			// Continue with lock acquisition attempt
		}

		now := time.Now().UTC()
		myLockInfo := LockInfo{
			Hostname:  currentHostname,
			PID:       currentPID,
			ExpiresAt: now.Add(lockTTL),
		}
		myLockContent := myLockInfo.Format()

		var existingETag *string
		getObjectOutput, getErr := s3Client.GetObject(overallTimeoutCtx, &s3.GetObjectInput{
			Bucket: aws.String(c.s3Config.Bucket),
			Key:    aws.String(lockFile),
		})

		if getErr != nil {
			var nsk *types.NoSuchKey
			if errors.As(getErr, &nsk) {
				// Lock does not exist, try to create it.
				log.Printf("Lock attempt %d: No existing lock ('%s'). Attempting to create.", attempt, lockFile)
				_, putErr := s3Client.PutObject(overallTimeoutCtx, &s3.PutObjectInput{
					Bucket: aws.String(c.s3Config.Bucket),
					Key:    aws.String(lockFile),
					Body:   strings.NewReader(myLockContent),
				})
				if putErr == nil {
					log.Printf("Lock acquired by %s (PID %d) on attempt %d (created new). Expires at %v.", myLockInfo.Hostname, myLockInfo.PID, attempt, myLockInfo.ExpiresAt.Format(time.RFC3339))
					c.lockAcquired = true
					return nil // Lock acquired
				}
				log.Printf("Lock attempt %d: Failed to create lock after NoSuchKey: %v.", attempt, putErr)
			} else {
				log.Printf("Lock attempt %d: Error getting lock object: %v.", attempt, getErr)
			}
		} else { // Lock object exists
			func() { // Use a func scope to ensure defer getObjectOutput.Body.Close() is called promptly
				defer getObjectOutput.Body.Close()
				existingLockBytes, readErr := io.ReadAll(getObjectOutput.Body)
				if readErr != nil {
					log.Printf("Lock attempt %d: Failed to read existing lock content: %v.", attempt, readErr)
					// Fall through to sleep and retry without further action in this iteration
					return
				}

				existingETag = getObjectOutput.ETag
				existingLockInfo, parseErr := ParseLockInfo(string(existingLockBytes))

				shouldOverwrite := false
				if parseErr != nil {
					log.Printf("Lock attempt %d: Failed to parse existing lock content ('%s'): %v. Assuming corrupted/stale, will attempt to overwrite.", attempt, string(existingLockBytes), parseErr)
					shouldOverwrite = true
				} else if now.After(existingLockInfo.ExpiresAt) {
					log.Printf("Lock attempt %d: Existing lock by %s (PID %d) expired at %v. Will attempt to overwrite.", attempt, existingLockInfo.Hostname, existingLockInfo.PID, existingLockInfo.ExpiresAt.Format(time.RFC3339))
					shouldOverwrite = true
				}

				if shouldOverwrite {
					log.Printf("Lock attempt %d: Attempting to overwrite previous lock (ETag: %s).", attempt, aws.ToString(existingETag))
					_, overwriteErr := s3Client.PutObject(overallTimeoutCtx, &s3.PutObjectInput{
						Bucket:  aws.String(c.s3Config.Bucket),
						Key:     aws.String(lockFile),
						Body:    strings.NewReader(myLockContent),
						IfMatch: existingETag, // Crucial: only overwrite if ETag matches what we read.
					})
					if overwriteErr == nil {
						log.Printf("Lock acquired by %s (PID %d) on attempt %d (overwrote previous lock). Expires at %v.", myLockInfo.Hostname, myLockInfo.PID, attempt, myLockInfo.ExpiresAt.Format(time.RFC3339))
						c.lockAcquired = true
						// return from acquireLock (via outer function)
						// This requires a way to signal success out of the func scope, or restructure.
						// For now, we'll let the outer loop handle the return on c.lockAcquired
					} else {
						log.Printf("Lock attempt %d: Failed to overwrite previous lock (ETag %s): %v.", attempt, aws.ToString(existingETag), overwriteErr)
					}
				} else {
					// Lock exists, is valid, and not expired by current holder.
					log.Printf("Lock attempt %d: Lock currently held by %s (PID %d), expires at %v. Will retry.", attempt, existingLockInfo.Hostname, existingLockInfo.PID, existingLockInfo.ExpiresAt.Format(time.RFC3339))
					if !retryOnLockHeld {
						// Store lock owner info for the error message
						c.lockAcquired = false
						c.lockOwner = fmt.Sprintf("%s (PID %d)", existingLockInfo.Hostname, existingLockInfo.PID)
						return
					}
				}
			}() // End of func scope for GetObject body handling

			if c.lockAcquired { // Check if lock was acquired within the func scope
				return nil
			}
		}

		// Check if we should return an error due to lock being held
		if !retryOnLockHeld && !c.lockAcquired {
			return fmt.Errorf("lock held by another process: %s", c.lockOwner)
		}

		// Wait before next attempt or before timeout check
		select {
		case <-time.After(lockAcquireInterval):
			// continue to next iteration
		case <-overallTimeoutCtx.Done():
			// Timeout occurred during sleep, exit loop.
			// The check at the beginning of the loop will catch this.
		}
	}
}

// releaseLock attempts to release a previously acquired S3 lock.
// It verifies ownership by checking hostname and PID before deleting.
func (c *Admin) releaseLock(ctx context.Context) {
	// rule: Only delete the lock if this instance believes it acquired it and can verify ownership.
	c.mu.Lock() // Ensure thread-safe access to c.lockAcquired
	if !c.lockAcquired {
		c.mu.Unlock()
		log.Printf("Skipping lock release: lock was not recorded as acquired by this instance.")
		return
	}
	c.mu.Unlock() // Unlock early if we are proceeding

	currentHostname, err := os.Hostname()
	if err != nil {
		currentHostname = "unknown-host" // Fallback
		log.Printf("Warning: could not get hostname for lock release: %v", err)
	}
	currentPID := os.Getpid()

	s3Client := s3.NewFromConfig(c.s3cfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})

	log.Printf("Attempting to release lock '%s' for %s (PID %d)...", lockFile, currentHostname, currentPID)

	// Use a shorter timeout for release attempt
	releaseCtx, cancelRelease := context.WithTimeout(ctx, 30*time.Second)
	defer cancelRelease()

	getObjectOutput, getErr := s3Client.GetObject(releaseCtx, &s3.GetObjectInput{
		Bucket: aws.String(c.s3Config.Bucket),
		Key:    aws.String(lockFile),
	})

	if getErr != nil {
		var nsk *types.NoSuchKey
		if errors.As(getErr, &nsk) {
			log.Printf("Lock '%s' not found during release. Already released or never existed.", lockFile)
		} else {
			log.Printf("Error getting lock object '%s' during release: %v", lockFile, getErr)
		}
		c.mu.Lock()
		c.lockAcquired = false // Assume it's gone or we can't verify
		c.mu.Unlock()
		return
	}
	defer getObjectOutput.Body.Close()

	existingLockBytes, readErr := io.ReadAll(getObjectOutput.Body)
	if readErr != nil {
		log.Printf("Failed to read lock content for '%s' during release: %v. Not attempting delete.", lockFile, readErr)
		// Uncertain state, don't modify c.lockAcquired as we couldn't verify.
		return
	}

	existingLockInfo, parseErr := ParseLockInfo(string(existingLockBytes))
	if parseErr != nil {
		log.Printf("Failed to parse lock content for '%s' during release: %v. Content: '%s'. Not deleting.", lockFile, parseErr, string(existingLockBytes))
		// Don't delete if we can't parse it - it might not be ours.
		return
	}

	if existingLockInfo.Hostname == currentHostname && existingLockInfo.PID == currentPID {
		log.Printf("Lock content matches current process (%s, PID %d). Proceeding with delete of '%s'.", existingLockInfo.Hostname, existingLockInfo.PID, lockFile)
		_, delErr := s3Client.DeleteObject(releaseCtx, &s3.DeleteObjectInput{
			Bucket: aws.String(c.s3Config.Bucket),
			Key:    aws.String(lockFile),
			// S3 DeleteObject does not support IfMatch on ETag directly in standard SDK calls for non-versioned buckets.
			// For a truly conditional delete based on ETag without versioning, one might need to
			// use lower-level HTTP requests or ensure the bucket has versioning and delete a specific version.
			// Given this limitation, we are deleting after a read-check. The risk is a race where
			// the lock is replaced by another process between our read and delete.
		})

		c.mu.Lock()
		if delErr != nil {
			log.Printf("Failed to delete lock object '%s': %v", lockFile, delErr)
			// lockAcquired remains true as release failed.
		} else {
			log.Printf("Successfully deleted lock object '%s'.", lockFile)
			c.lockAcquired = false // Mark as released
		}
		c.mu.Unlock()
	} else {
		log.Printf("Lock content for '%s' does not match current process. Expected Host: %s, PID: %d. Found Host: %s, PID: %d, Expires: %v. Not deleting.",
			lockFile, currentHostname, currentPID, existingLockInfo.Hostname, existingLockInfo.PID, existingLockInfo.ExpiresAt.Format(time.RFC3339))
		c.mu.Lock()
		// If we thought we had the lock, but its content shows it belongs to someone else,
		// it implies our lock was overwritten or we are mistaken.
		if c.lockAcquired {
			log.Printf("Warning: This instance (%s, PID %d) believed it held the lock, but S3 content indicates otherwise. Marking lock as not acquired.", currentHostname, currentPID)
			c.lockAcquired = false
		}
		c.mu.Unlock()
	}
}

// handleStatus returns the current status of the controller
func (c *Admin) handleStatus(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	status := struct {
		Configured bool `json:"configured"`
		// Running    bool `json:"running"` // Supervisor status removed
	}{
		Configured: c.s3Config != nil,
		// Running:    false, // Supervisor status removed
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
		// Running    bool `json:"running"`
	}{
		Configured: c.s3Config != nil,
		// Running:    false,
	}
}

// GetStorageConfig returns the current object storage configuration
// This method needs to be thread-safe
func (c *Admin) GetStorageConfig() *ObjectStorageConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.s3Config
}

// WaitForLockCleanup waits for the lock refresh goroutine to complete.
// Note: With the current synchronous acquire/release, this WaitGroup might be for other purposes
// or a remnant of a previous async lock refresh design. If it's solely for this lock,
// and there's no background refresh, its usage here might need review.
func (c *Admin) WaitForLockCleanup() {
	log.Println("Waiting for lock cleanup...")
	c.lockWG.Wait() // This will block if Add() was called and Done() hasn't been called an equal number of times.
	log.Println("Lock cleanup finished.")
}
