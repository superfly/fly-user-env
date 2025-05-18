package lib

import (
	"context"
	"fmt"
	"os"
	"time"

	lss3 "github.com/benbjohnson/litestream/s3"
)

// LeaserComponent implements StackComponent for S3 lease management
type LeaserComponent struct {
	leaser *lss3.Leaser
	owner  string
}

func NewLeaserComponent() *LeaserComponent {
	return &LeaserComponent{
		owner: fmt.Sprintf("%s-%d", os.Getenv("HOSTNAME"), os.Getpid()),
	}
}

func (l *LeaserComponent) Setup(ctx context.Context, cfg *ObjectStorageConfig) error {
	leaser := lss3.NewLeaser()
	leaser.Bucket = cfg.Bucket
	leaser.Endpoint = cfg.Endpoint
	leaser.AccessKeyID = cfg.AccessKey
	leaser.SecretAccessKey = cfg.SecretKey
	leaser.Region = cfg.Region
	leaser.ForcePathStyle = true
	leaser.Path = "leases/fly.lock"
	leaser.Owner = l.owner
	leaser.LeaseTimeout = 5 * time.Minute

	if err := leaser.Open(); err != nil {
		return fmt.Errorf("failed to open leaser: %w", err)
	}

	l.leaser = leaser
	return nil
}

func (l *LeaserComponent) Cleanup(ctx context.Context) error {
	if l.leaser != nil {
		// Get all epochs to find active leases
		epochs, err := l.leaser.Epochs(ctx)
		if err != nil {
			return fmt.Errorf("failed to list epochs: %w", err)
		}

		// Release each lease
		for _, epoch := range epochs {
			if err := l.leaser.ReleaseLease(ctx, epoch); err != nil {
				return fmt.Errorf("failed to release lease %d: %w", epoch, err)
			}
		}

		l.leaser = nil
	}
	return nil
}
