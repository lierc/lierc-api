package stream

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/testenv"
)

// ended reports whether c's stream body reaches EOF within d.
func (c *client) ended(d time.Duration) bool {
	c.t.Helper()
	timeout := time.After(d)
	for {
		select {
		case _, ok := <-c.events:
			if !ok {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func TestStreamEndsWhenLoginDeleted(t *testing.T) {
	e := setup(t)
	e.s.LoginCheck = 100 * time.Millisecond
	user := e.addUser("alice")
	tok1 := testenv.Login(t, e.db, user)
	tok2 := testenv.Login(t, e.db, user)
	c1 := e.openToken(tok1)
	c2 := e.openToken(tok2)
	e.exec(`DELETE FROM login WHERE id = (SELECT min(id) FROM login WHERE "user" = $1)`, user)
	if !c1.ended(2 * time.Second) {
		t.Fatal("stream of the deleted login stayed open")
	}
	if c2.ended(500 * time.Millisecond) {
		t.Fatal("stream of the other login ended")
	}
}

func TestStreamEndsWhenUserDeleted(t *testing.T) {
	e := setup(t)
	e.s.LoginCheck = 100 * time.Millisecond
	user := e.addUser("bob")
	c := e.openToken(testenv.Login(t, e.db, user))
	e.exec(`DELETE FROM "user" WHERE id = $1`, user)
	if !c.ended(2 * time.Second) {
		t.Fatal("stream stayed open after its user was deleted")
	}
}

func TestStreamCheckIsReadOnly(t *testing.T) {
	e := setup(t)
	e.s.LoginCheck = 50 * time.Millisecond
	user := e.addUser("carol")
	c := e.openToken(testenv.Login(t, e.db, user))
	e.exec(`UPDATE login SET last_used = now() - interval '2 days' WHERE "user" = $1`, user)
	var before time.Time
	row := func() time.Time {
		var ts time.Time
		if err := e.db.Pool.QueryRow(context.Background(), `SELECT last_used FROM login WHERE "user" = $1`, user).Scan(&ts); err != nil {
			t.Fatal(err)
		}
		return ts
	}
	before = row()
	if c.ended(600 * time.Millisecond) {
		t.Fatal("stream ended")
	}
	if after := row(); !after.Equal(before) {
		t.Fatalf("last_used changed from %v to %v", before, after)
	}
}

func TestStreamCheckErrorKeepsStream(t *testing.T) {
	e := setup(t)
	e.s.LoginCheck = 50 * time.Millisecond
	user := e.addUser("dave")
	var checks atomic.Int32
	e.s.alive = func(ctx context.Context, id int64) (bool, error) {
		checks.Add(1)
		return false, errors.New("db down")
	}
	c := e.openToken(testenv.Login(t, e.db, user))
	if c.ended(600 * time.Millisecond) {
		t.Fatal("stream ended on a failed check")
	}
	if checks.Load() < 2 {
		t.Fatalf("%d checks, want several", checks.Load())
	}
}

// A check that hangs gives up after LoginCheckTimeout and the stream stays
// open, so a stuck query can't stall the stream's writes.
func TestStreamCheckTimesOut(t *testing.T) {
	e := setup(t)
	e.s.LoginCheck = 50 * time.Millisecond
	e.s.LoginCheckTimeout = 100 * time.Millisecond
	user := e.addUser("erin")
	gaveUp := make(chan error, 10)
	e.s.alive = func(ctx context.Context, id int64) (bool, error) {
		<-ctx.Done()
		gaveUp <- ctx.Err()
		return false, ctx.Err()
	}
	c := e.openToken(testenv.Login(t, e.db, user))
	select {
	case err := <-gaveUp:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("check ended with %v, want its deadline", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a hung check never gave up")
	}
	if c.ended(300 * time.Millisecond) {
		t.Fatal("stream ended after a timed-out check")
	}
}
