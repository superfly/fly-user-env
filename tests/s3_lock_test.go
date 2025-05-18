package tests

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"supervisor/lib"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3LockIfMatchBehavior(t *testing.T) {
	if os.Getenv("INTEGRATION_TEST") != "1" {
		t.Skip("Skipping integration test")
	}

	bucket := os.Getenv("FLY_TIGRIS_BUCKET")
	endpoint := os.Getenv("FLY_TIGRIS_ENDPOINT_URL")
	accessKey := os.Getenv("FLY_TIGRIS_ACCESS_KEY")
	secretKey := os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY")
	region := os.Getenv("FLY_TIGRIS_REGION")

	client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		UsePathStyle: true,
	})

	ctx := context.Background()
	lockKey := "fly.lock"

	// Test 1: Basic lock acquisition and content format
	t.Run("Basic lock acquisition", func(t *testing.T) {
		hostname, _ := os.Hostname()
		pid := os.Getpid()
		expiresAt := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)
		lockInfo := lib.LockInfo{
			Hostname:  hostname,
			PID:       pid,
			ExpiresAt: expiresAt,
		}
		lockContent := lockInfo.Format()

		// Create the lock
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey),
			Body:   strings.NewReader(lockContent),
		})
		if err != nil {
			t.Fatalf("Failed to create lock: %v", err)
		}

		// Verify lock content
		getOut, err := client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey),
		})
		if err != nil {
			t.Fatalf("Failed to get lock: %v", err)
		}
		defer getOut.Body.Close()

		content, err := io.ReadAll(getOut.Body)
		if err != nil {
			t.Fatalf("Failed to read lock content: %v", err)
		}

		parsedInfo, err := lib.ParseLockInfo(string(content))
		if err != nil {
			t.Fatalf("Failed to parse lock content: %v", err)
		}

		// Log the actual lock structs
		t.Logf("Expected LockInfo: %+v", lockInfo)
		t.Logf("Actual LockInfo: %+v", parsedInfo)

		if parsedInfo.Hostname != hostname {
			t.Errorf("Expected hostname %s, got %s", hostname, parsedInfo.Hostname)
		}
		if parsedInfo.PID != pid {
			t.Errorf("Expected PID %d, got %d", pid, parsedInfo.PID)
		}
		if !parsedInfo.ExpiresAt.Equal(expiresAt) {
			t.Errorf("Expected expiry %v, got %v", expiresAt, parsedInfo.ExpiresAt)
		}
	})

	// Test 2: Lock expiration
	t.Run("Lock expiration", func(t *testing.T) {
		// Create an expired lock
		hostname, _ := os.Hostname()
		pid := os.Getpid()
		expiresAt := time.Now().Add(-1 * time.Minute).Truncate(time.Second) // Expired 1 minute ago
		lockInfo := lib.LockInfo{
			Hostname:  hostname,
			PID:       pid,
			ExpiresAt: expiresAt,
		}
		lockContent := lockInfo.Format()

		// Create the expired lock
		putOut, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey),
			Body:   strings.NewReader(lockContent),
		})
		if err != nil {
			t.Fatalf("Failed to create expired lock: %v", err)
		}
		etag := aws.ToString(putOut.ETag)

		// Try to acquire the expired lock
		newExpiresAt := time.Now().Add(5 * time.Minute).Truncate(time.Second)
		newLockInfo := lib.LockInfo{
			Hostname:  hostname,
			PID:       pid + 1, // Different PID
			ExpiresAt: newExpiresAt,
		}
		newLockContent := newLockInfo.Format()

		// Should succeed because lock is expired
		_, err = client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:  aws.String(bucket),
			Key:     aws.String(lockKey),
			Body:    strings.NewReader(newLockContent),
			IfMatch: aws.String(etag),
		})
		if err != nil {
			t.Fatalf("Failed to acquire expired lock: %v", err)
		}

		// Verify new lock content
		getOut, err := client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey),
		})
		if err != nil {
			t.Fatalf("Failed to get new lock: %v", err)
		}
		defer getOut.Body.Close()

		content, err := io.ReadAll(getOut.Body)
		if err != nil {
			t.Fatalf("Failed to read new lock content: %v", err)
		}

		parsedInfo, err := lib.ParseLockInfo(string(content))
		if err != nil {
			t.Fatalf("Failed to parse new lock content: %v", err)
		}

		// Log the actual lock structs
		t.Logf("Expected New LockInfo: %+v", newLockInfo)
		t.Logf("Actual New LockInfo: %+v", parsedInfo)

		if parsedInfo.PID != pid+1 {
			t.Errorf("Expected new PID %d, got %d", pid+1, parsedInfo.PID)
		}
		if !parsedInfo.ExpiresAt.Equal(newExpiresAt) {
			t.Errorf("Expected new expiry %v, got %v", newExpiresAt, parsedInfo.ExpiresAt)
		}
	})

	// Test 3: Lock held by another process
	t.Run("Lock held by another process", func(t *testing.T) {
		hostname, _ := os.Hostname()
		pid := os.Getpid()
		expiresAt := time.Now().Add(5 * time.Minute).Truncate(time.Second)
		lockInfo := lib.LockInfo{
			Hostname:  hostname,
			PID:       pid,
			ExpiresAt: expiresAt,
		}
		lockContent := lockInfo.Format()

		// Create the lock
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey),
			Body:   strings.NewReader(lockContent),
		})
		if err != nil {
			t.Fatalf("Failed to create lock: %v", err)
		}

		// Attempt to acquire the lock with a different PID
		newPid := pid + 1
		newLockInfo := lib.LockInfo{
			Hostname:  hostname,
			PID:       newPid,
			ExpiresAt: expiresAt,
		}
		newLockContent := newLockInfo.Format()

		// Should fail because lock is held by another process
		_, err = client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey),
			Body:   strings.NewReader(newLockContent),
		})
		if err == nil {
			t.Error("Expected error when acquiring lock held by another process")
		}
	})

	// Test 4: Invalid lock content
	t.Run("Invalid lock content", func(t *testing.T) {
		// Create lock with invalid content
		invalidContent := "invalid:content:format"
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey),
			Body:   strings.NewReader(invalidContent),
		})
		if err != nil {
			t.Fatalf("Failed to create invalid lock: %v", err)
		}

		// Try to parse invalid content
		getOut, err := client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey),
		})
		if err != nil {
			t.Fatalf("Failed to get invalid lock: %v", err)
		}
		defer getOut.Body.Close()

		content, err := io.ReadAll(getOut.Body)
		if err != nil {
			t.Fatalf("Failed to read invalid lock content: %v", err)
		}

		_, err = lib.ParseLockInfo(string(content))
		if err == nil {
			t.Error("Expected error when parsing invalid lock content")
		}

		// Clean up
		_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey),
		})
		if err != nil {
			t.Logf("Failed to clean up invalid lock: %v", err)
		}
	})
}

func TestS3LockWithKeyPrefix(t *testing.T) {
	if os.Getenv("INTEGRATION_TEST") != "1" {
		t.Skip("Skipping integration test")
	}

	bucket := os.Getenv("FLY_TIGRIS_BUCKET")
	endpoint := os.Getenv("FLY_TIGRIS_ENDPOINT_URL")
	accessKey := os.Getenv("FLY_TIGRIS_ACCESS_KEY")
	secretKey := os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY")
	region := os.Getenv("FLY_TIGRIS_REGION")

	client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		UsePathStyle: true,
	})

	ctx := context.Background()
	lockKey1 := "test-1/fly.lock"
	lockKey2 := "test-2/fly.lock"

	// Create valid lock content
	hostname, _ := os.Hostname()
	pid := os.Getpid()
	expiresAt := time.Now().Add(5 * time.Minute).Truncate(time.Second)
	lockInfo := lib.LockInfo{
		Hostname:  hostname,
		PID:       pid,
		ExpiresAt: expiresAt,
	}
	lockContent := lockInfo.Format()

	// Test 1: Acquire lock with key prefix 'test-1/'
	t.Run("Lock with prefix test-1", func(t *testing.T) {
		putOut1, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey1),
			Body:   strings.NewReader(lockContent),
		})
		if err != nil {
			t.Fatalf("Failed to create lock with prefix 'test-1/': %v", err)
		}
		t.Logf("Lock created with prefix 'test-1/', ETag: %s", aws.ToString(putOut1.ETag))

		// Verify lock content
		getOut, err := client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey1),
		})
		if err != nil {
			t.Fatalf("Failed to get lock: %v", err)
		}
		defer getOut.Body.Close()

		content, err := io.ReadAll(getOut.Body)
		if err != nil {
			t.Fatalf("Failed to read lock content: %v", err)
		}

		parsedInfo, err := lib.ParseLockInfo(string(content))
		if err != nil {
			t.Fatalf("Failed to parse lock content: %v", err)
		}

		// Log the actual lock structs
		t.Logf("Expected LockInfo: %+v", lockInfo)
		t.Logf("Actual LockInfo: %+v", parsedInfo)

		if parsedInfo.Hostname != hostname {
			t.Errorf("Expected hostname %s, got %s", hostname, parsedInfo.Hostname)
		}
		if parsedInfo.PID != pid {
			t.Errorf("Expected PID %d, got %d", pid, parsedInfo.PID)
		}
	})

	// Test 2: Attempt to acquire lock again with the same prefix
	t.Run("Lock with same prefix", func(t *testing.T) {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey1),
			Body:   strings.NewReader(lockContent),
		})
		if err == nil {
			t.Fatalf("Expected failure when acquiring lock with same prefix 'test-1/'")
		}
		t.Logf("Lock acquisition with same prefix 'test-1/' failed as expected: %v", err)
	})

	// Test 3: Acquire lock with a different key prefix 'test-2/'
	t.Run("Lock with different prefix", func(t *testing.T) {
		putOut2, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey2),
			Body:   strings.NewReader(lockContent),
		})
		if err != nil {
			t.Fatalf("Failed to create lock with prefix 'test-2/': %v", err)
		}
		t.Logf("Lock created with prefix 'test-2/', ETag: %s", aws.ToString(putOut2.ETag))
	})

	// Clean up
	t.Run("Cleanup", func(t *testing.T) {
		_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey1),
		})
		if err != nil {
			t.Logf("Failed to delete lock with prefix 'test-1/': %v", err)
		}
		_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey2),
		})
		if err != nil {
			t.Logf("Failed to delete lock with prefix 'test-2/': %v", err)
		}
		t.Logf("Cleanup completed for test-*/ locks")
	})
}
