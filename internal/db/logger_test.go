package db_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestNotifyTarget(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// u1 confirmed, email on, two push subscriptions; u2 email off; u4
	// from before confirmation (verified NULL); u5 not confirmed
	for _, c := range []string{"c1", "c2", "c4", "c5"} {
		testenv.EnsureConnection(t, d, c, "u"+c[1:])
	}
	exec(`UPDATE users SET verified = NULL WHERE id = 'u4'`)
	exec(`UPDATE users SET verified = false WHERE id = 'u5'`)
	for _, u := range []string{"u1", "u4", "u5"} {
		exec(`INSERT INTO prefs (user_id, name, value) VALUES ($1, 'email', 'true')`, u)
	}
	exec(`INSERT INTO prefs (user_id, name, value) VALUES ('u2', 'email', 'false')`)
	exec(`INSERT INTO web_push_subscriptions (endpoint, key, auth, user_id) VALUES
		('https://push.example/b', 'kb', 'ab', 'u1'), ('https://push.example/a', 'ka', 'aa', 'u1')`)

	tg, ok, err := d.NotifyTarget(ctx, "c1")
	email := one[string](t, d, `SELECT email FROM users WHERE id = 'u1'`)
	if err != nil || !ok || tg.User != "u1" || tg.Email != email || !tg.EmailOn || len(tg.WebPush) != 2 ||
		tg.WebPush[0] != (db.WebPushSubscription{Endpoint: "https://push.example/a", Key: "ka", Auth: "aa"}) {
		t.Errorf("c1: %+v %v %v", tg, ok, err)
	}
	if tg, ok, err := d.NotifyTarget(ctx, "c2"); err != nil || !ok || tg.EmailOn || len(tg.WebPush) != 0 {
		t.Errorf("c2: %+v %v %v", tg, ok, err)
	}
	if tg, _, err := d.NotifyTarget(ctx, "c4"); err != nil || !tg.EmailOn {
		t.Errorf("c4 (verified NULL): %+v %v", tg, err)
	}
	if tg, _, err := d.NotifyTarget(ctx, "c5"); err != nil || tg.EmailOn {
		t.Errorf("c5 (unconfirmed): %+v %v", tg, err)
	}
	if _, ok, err := d.NotifyTarget(ctx, "nope"); ok || err != nil {
		t.Errorf("nope: %v %v", ok, err)
	}
}

func TestHighlightWords(t *testing.T) {
	d := testenv.Postgres(t)
	testenv.AddConnection(t, d, "c1", "u1", `{"Highlight": ["leedo"]}`)
	testenv.AddConnection(t, d, "c2", "u1", `{}`)
	w, err := d.HighlightWords(context.Background())
	if err != nil || len(w) != 1 || w["c1"] != `["leedo"]` {
		t.Errorf("HighlightWords = %v, %v", w, err)
	}
}

func TestSavePrivateAt(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	testenv.EnsureConnection(t, d, "c1", "u1")
	for _, at := range []float64{1700000000, 1700000100} {
		if err := d.SavePrivateAt(ctx, "c1", "bob", at); err != nil {
			t.Fatal(err)
		}
	}
	if got := one[int64](t, d, `SELECT extract(epoch FROM time)::bigint FROM private_chats WHERE connection_id = 'c1' AND nick = 'bob'`); got != 1700000100 {
		t.Errorf("time %d", got)
	}
	// no such connection: nothing stored, no error
	if err := d.SavePrivateAt(ctx, "nope", "bob", 1700000000); err != nil {
		t.Errorf("missing connection: %v", err)
	}
}

func TestInsertLogMissingConnection(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	e := db.LogEntry{Connection: "nope", Command: "PRIVMSG", Message: []byte(`{}`), Time: 1700000000}
	for _, channels := range [][]string{{"#a"}, {"#a", "#b"}} {
		if _, err := d.InsertLog(ctx, e, channels, false); !errors.Is(err, db.ErrGone) {
			t.Errorf("InsertLog %v: %v, want ErrGone", channels, err)
		}
	}
	if id, err := d.InsertLog(ctx, e, nil, false); id != 0 || err != nil {
		t.Errorf("InsertLog, no channels: %d %v", id, err)
	}
}

// listening starts ListenHighlights and returns how often changed was
// called, and the reported errors.
func listening(t *testing.T, d *db.DB, retry time.Duration) (*atomic.Int32, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	errs := make(chan error, 100)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.ListenHighlights(ctx, retry, func() { calls.Add(1) }, func(err error) { errs <- err })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("ListenHighlights didn't return after cancel")
		}
	})
	return &calls, errs
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The listener's backend, by the LISTEN it holds.
func listenerPID(t *testing.T, d *db.DB) int32 {
	t.Helper()
	var pid int32
	waitFor(t, "a listening backend", func() bool {
		err := d.Pool.QueryRow(context.Background(),
			`SELECT pid FROM pg_stat_activity WHERE datname = current_database() AND application_name = $1`, db.ListenerName).Scan(&pid)
		return err == nil
	})
	return pid
}

func TestListenHighlights(t *testing.T) {
	// a cleanup, not a defer: it must run after listening's, which stops
	// the listener
	t.Cleanup(db.SetListenPing(50 * time.Millisecond))
	d := testenv.Postgres(t)
	ctx := context.Background()
	calls, errs := listening(t, d, 10*time.Millisecond)

	// once on listening
	waitFor(t, "the first call", func() bool { return calls.Load() == 1 })
	pid := listenerPID(t, d)

	// pings in between keep the same connection, without calls
	time.Sleep(300 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Errorf("%d calls while idle, want 1", n)
	}
	if got := listenerPID(t, d); got != pid {
		t.Errorf("listener moved from backend %d to %d while idle", pid, got)
	}

	if err := d.NotifyHighlights(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the NOTIFY's call", func() bool { return calls.Load() == 2 })

	// a lost connection is reported, then it listens again, and calls
	// changed for the NOTIFYs it may have missed
	if _, err := d.Pool.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the call after reconnecting", func() bool { return calls.Load() == 3 })
	select {
	case err := <-errs:
		t.Logf("reported: %v", err)
	default:
		t.Error("lost connection not reported")
	}
	if got := listenerPID(t, d); got == pid {
		t.Error("still on the terminated backend")
	}
	if err := d.NotifyHighlights(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a NOTIFY after reconnecting", func() bool { return calls.Load() == 4 })
}
