package tests

import (
	"context"
	"os"
	"strings"
	"testing"

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
	})

	ctx := context.Background()
	lockKey := "fly.lock"
	lockBody := strings.NewReader("locked")

	// 1. Acquire the lock (create the lock file)
	putOut, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(lockKey),
		Body:   lockBody,
	})
	if err != nil {
		t.Fatalf("Failed to create lock: %v", err)
	}
	etag := aws.ToString(putOut.ETag)
	t.Logf("Lock created, ETag: %s", etag)

	// 2. Attempt to acquire the lock again (should succeed, since no If-Match is used)
	lockBody2 := strings.NewReader("locked2")
	putOut2, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(lockKey),
		Body:   lockBody2,
	})
	if err != nil {
		t.Fatalf("Unexpected error on second PutObject: %v", err)
	}
	t.Logf("Lock overwritten, new ETag: %s", aws.ToString(putOut2.ETag))

	// 3. Attempt to overwrite the lock file with If-Match set to the original ETag (should fail if ETag changed, but our code does not use If-Match)
	lockBody3 := strings.NewReader("locked3")
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:  aws.String(bucket),
		Key:     aws.String(lockKey),
		Body:    lockBody3,
		IfMatch: putOut.ETag, // Use the original ETag
	})
	if err == nil {
		t.Logf("PutObject with If-Match succeeded (no lock protection in place)")
	} else {
		t.Logf("PutObject with If-Match failed as expected: %v", err)
	}

	// Clean up
	_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(lockKey),
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
	})

	ctx := context.Background()
	lockKey1 := "test-1/fly.lock"
	lockKey2 := "test-2/fly.lock"
	lockBody := strings.NewReader("locked")

	// 1. Acquire lock with key prefix 'test-1/'
	putOut1, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:  aws.String(bucket),
		Key:     aws.String(lockKey1),
		Body:    lockBody,
		IfMatch: aws.String(""), // Only succeed if file does not exist
	})
	if err != nil {
		t.Fatalf("Failed to create lock with prefix 'test-1/': %v", err)
	}
	t.Logf("Lock created with prefix 'test-1/', ETag: %s", aws.ToString(putOut1.ETag))

	// 2. Attempt to acquire lock again with the same prefix (should fail)
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:  aws.String(bucket),
		Key:     aws.String(lockKey1),
		Body:    lockBody,
		IfMatch: aws.String(""), // Only succeed if file does not exist
	})
	if err == nil {
		t.Fatalf("Expected failure when acquiring lock with same prefix 'test-1/'")
	}
	t.Logf("Lock acquisition with same prefix 'test-1/' failed as expected: %v", err)

	// 3. Acquire lock with a different key prefix 'test-2/'
	putOut2, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:  aws.String(bucket),
		Key:     aws.String(lockKey2),
		Body:    lockBody,
		IfMatch: aws.String(""), // Only succeed if file does not exist
	})
	if err != nil {
		t.Fatalf("Failed to create lock with prefix 'test-2/': %v", err)
	}
	t.Logf("Lock created with prefix 'test-2/', ETag: %s", aws.ToString(putOut2.ETag))

	// 4. Clean up: Delete all objects with the test-*/ prefix
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
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
}
