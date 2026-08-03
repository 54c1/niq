package helper

import (
	"context"
	"time"
)

// Retry executes fn up to maxRetries times with exponential backoff.
// fn returns (shouldRetry, error). If shouldRetry is true and error is
// non-nil, retries after backoff. Otherwise stops immediately.
func Retry(ctx context.Context, maxRetries int, fn func() (bool, error)) error {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		shouldRetry, err := fn()
		if err == nil {
			return nil
		}
		if !shouldRetry || attempt == maxRetries {
			return err
		}
		backoff := time.Duration(1<<uint(attempt)) * time.Second
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
