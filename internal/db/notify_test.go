package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestLookupConnection(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "ann")
	testenv.AddConnection(t, d, "c1", u, `{"Host":"irc.libera.chat","Alias":"Libera","Nick":"ann"}`)

	owner, cfg, ok, err := d.LookupConnection(context.Background(), "c1")
	if err != nil || !ok || owner != u || string(cfg) == "" {
		t.Fatalf("c1: owner %q cfg %s ok %v err %v", owner, cfg, ok, err)
	}
	if _, _, ok, err := d.LookupConnection(context.Background(), "nope"); err != nil || ok {
		t.Fatalf("unknown: ok %v err %v", ok, err)
	}
}

// The delete notification goes out when the transaction commits, and
// not at all when it rolls back.
func TestNotifyConnectionDeletesOnCommit(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "ann")
	testenv.AddConnection(t, d, "c1", u, `{}`)
	testenv.AddConnection(t, d, "c2", u, `{}`)
	changes := testenv.ListenConnections(t, d)
	ctx := context.Background()

	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.NotifyConnectionDeletes(ctx, tx, "id = $1", "c1"); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(ctx)
	if got := changes(300 * time.Millisecond); len(got) != 0 {
		t.Fatalf("rolled back, but notified %v", got)
	}

	tx, err = d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.NotifyConnectionDeletes(ctx, tx, `"user" = $1`, u); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := changes(2 * time.Second)
	if len(got) != 2 {
		t.Fatalf("notified %v, want c1 and c2", got)
	}
	seen := map[string]bool{}
	for _, c := range got {
		if c.User != u || c.Action != "delete" {
			t.Errorf("change %+v", c)
		}
		seen[c.Connection] = true
	}
	if !seen["c1"] || !seen["c2"] {
		t.Errorf("notified %v", got)
	}
}
