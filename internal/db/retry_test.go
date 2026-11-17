package db

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// After the last try, retryDeadlock returns at once instead of sleeping
// first.
func TestRetryDeadlockNoSleepAfterLastTry(t *testing.T) {
	tries := 0
	start := time.Now()
	err := retryDeadlock(context.Background(), func() error {
		tries++
		return &pgconn.PgError{Code: "40P01"}
	})
	took := time.Since(start)
	if !isCode(err, "40P01") || tries != deadlockTries {
		t.Fatalf("err %v after %d tries", err, tries)
	}
	if want := time.Duration(deadlockTries-1) * 100 * time.Millisecond; took > want+60*time.Millisecond {
		t.Errorf("took %v, want about %v (no sleep after the last try)", took, want)
	}
}
