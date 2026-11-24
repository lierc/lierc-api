package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

func one[T any](t *testing.T, d *db.DB, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := d.Pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSetPref(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	for _, v := range []string{`"light"`, `"dark"`} {
		if err := d.SetPref(ctx, a, "theme", v); err != nil {
			t.Fatal(err)
		}
	}
	if got := one[string](t, d, `SELECT string_agg(value, ',') FROM prefs WHERE user_id=$1`, a); got != `"dark"` {
		t.Errorf("prefs = %s", got)
	}
}

func TestSetSeen(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	testenv.ChannelID(t, d, "c1", "#a") // something was logged there
	if err := d.SetSeen(ctx, a, "c1", "#a", 5); err != nil {
		t.Fatal(err)
	}
	exec(t, d, `UPDATE seen_markers SET updated='2000-01-01'`)
	if err := d.SetSeen(ctx, a, "c1", "#a", 9); err != nil {
		t.Fatal(err)
	}
	if got := one[string](t, d, `SELECT count(*) || ' ' || max(message_id) || ' ' || (max(updated) > '2001-01-01') FROM seen_markers`); got != "1 9 true" {
		t.Errorf("seen = %s", got)
	}
}

func TestPrivateAddDelete(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	testenv.AddConnection(t, d, "c1", "u1", "{}")
	if err := d.AddPrivate(ctx, "c1", "bob"); err != nil {
		t.Fatal(err)
	}
	exec(t, d, `UPDATE private_chats SET time='2000-01-01'`)
	if err := d.AddPrivate(ctx, "c1", "bob"); err != nil {
		t.Fatal(err)
	}
	if got := one[string](t, d, `SELECT count(*) || ' ' || (max(time) > '2001-01-01') FROM private_chats`); got != "1 true" {
		t.Errorf("private = %s", got)
	}
	for i := 0; i < 2; i++ {
		if err := d.DeletePrivate(ctx, "c1", "bob"); err != nil {
			t.Fatal(err)
		}
	}
	if n := one[int64](t, d, `SELECT count(*) FROM private_chats`); n != 0 {
		t.Errorf("%d rows left", n)
	}
}

func TestWebPushSaveDelete(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a, b := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob")
	const ep = "https://fcm.googleapis.com/x"
	for _, k := range []string{"k1", "k2"} {
		if err := d.SaveWebPush(ctx, a, ep, k, "a-"+k); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SaveWebPush(ctx, b, ep, "kb", "ab"); err != nil {
		t.Fatal(err)
	}
	if got := one[string](t, d, `SELECT count(*) || ' ' || max(key) || ' ' || max(auth) FROM web_push_subscriptions WHERE user_id=$1`, a); got != "1 k2 a-k2" {
		t.Errorf("alice's web push = %s", got)
	}
	if err := d.DeleteWebPush(ctx, a, ep); err != nil {
		t.Fatal(err)
	}
	if got := one[string](t, d, `SELECT string_agg(user_id, ',') FROM web_push_subscriptions`); got != b {
		t.Errorf("left = %s, want only bob's", got)
	}
}

// A write whose user or connection is gone (deleted after the API checked
// it) returns ErrGone and writes nothing.
func TestWritesForGoneOwners(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	for name, err := range map[string]error{
		"SetSeen":     d.SetSeen(ctx, "nouser", "noconn", "#a", 1),
		"AddPrivate":  d.AddPrivate(ctx, "noconn", "bob"),
		"SetPref":     d.SetPref(ctx, "nouser", "p", "v"),
		"SaveWebPush": d.SaveWebPush(ctx, "nouser", "https://e", "k", "a"),
	} {
		if !errors.Is(err, db.ErrGone) {
			t.Errorf("%s: err = %v, want ErrGone", name, err)
		}
	}
	if n := one[int](t, d, `SELECT (SELECT count(*) FROM seen_markers) + (SELECT count(*) FROM private_chats)
		+ (SELECT count(*) FROM prefs) + (SELECT count(*) FROM web_push_subscriptions)`); n != 0 {
		t.Errorf("%d rows written", n)
	}
}
