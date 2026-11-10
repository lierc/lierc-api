package testenv

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/db"
)

// ListenConnections LISTENs on the connections channel of d and returns a
// function that collects the changes notified within wait.
func ListenConnections(t testing.TB, d *db.DB) func(wait time.Duration) []db.ConnectionChange {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := d.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "LISTEN "+db.ConnectionsChannel); err != nil {
		t.Fatal(err)
	}
	got := make(chan db.ConnectionChange, 100)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			n, err := conn.Conn().WaitForNotification(ctx)
			if err != nil {
				return
			}
			var c db.ConnectionChange
			if err := json.Unmarshal([]byte(n.Payload), &c); err != nil {
				c = db.ConnectionChange{Action: "unparseable: " + n.Payload}
			}
			got <- c
		}
	}()
	t.Cleanup(func() { cancel(); <-done; conn.Release() })
	return func(wait time.Duration) []db.ConnectionChange {
		var out []db.ConnectionChange
		timeout := time.After(wait)
		for {
			select {
			case c := <-got:
				out = append(out, c)
			case <-timeout:
				return out
			}
		}
	}
}
