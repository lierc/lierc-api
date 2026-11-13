package db_test

import (
	"context"
	"testing"

	"github.com/lierc/lierc-api/internal/testenv"
)

func TestLoginInsertLookupTouch(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	h := []byte("hash-1")
	if err := d.InsertLogin(ctx, h, a, "agent", ""); err != nil {
		t.Fatal(err)
	}
	if got := one[bool](t, d, `SELECT ip IS NULL FROM login`); !got {
		t.Error("empty ip not stored as NULL")
	}
	l, ok, err := d.LoginByHash(ctx, h)
	if err != nil || !ok || l.User != a || l.Unverified {
		t.Fatalf("lookup = %+v %v %v", l, ok, err)
	}
	if _, ok, err := d.LoginByHash(ctx, []byte("other")); ok || err != nil {
		t.Errorf("unknown hash: %v %v", ok, err)
	}
	exec(t, d, `UPDATE "user" SET verified=false`)
	if l, _, _ := d.LoginByHash(ctx, h); !l.Unverified {
		t.Error("unverified user not shown")
	}
	exec(t, d, `UPDATE login SET last_used='2000-01-01'`)
	if _, ok, _ := d.LoginByHash(ctx, h); ok {
		t.Error("expired login found")
	}
	if alive, err := d.LoginAlive(ctx, l.ID); alive || err != nil {
		t.Errorf("alive = %v %v", alive, err)
	}
	if err := d.TouchLogin(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	if alive, _ := d.LoginAlive(ctx, l.ID); !alive {
		t.Error("touched login not alive")
	}
}

func TestLoginDeleteListPrune(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a, b := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob")
	for i, u := range []string{a, a, a, b} {
		if err := d.InsertLogin(ctx, []byte{byte(i)}, u, "agent", "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	infos, err := d.Logins(ctx, a)
	if err != nil || len(infos) != 3 || infos[0].IP != "192.0.2.1" || infos[0].UserAgent != "agent" {
		t.Fatalf("logins = %+v %v", infos, err)
	}
	if ok, err := d.DeleteLogin(ctx, b, infos[0].ID); ok || err != nil {
		t.Errorf("deleted another user's login: %v %v", ok, err)
	}
	if ok, err := d.DeleteLogin(ctx, a, infos[0].ID); !ok || err != nil {
		t.Errorf("delete own: %v %v", ok, err)
	}
	for i := 0; i < 2; i++ {
		if err := d.DeleteLoginByHash(ctx, []byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	exec(t, d, `UPDATE login SET last_used='2000-01-01' WHERE "user"=$1`, b)
	if n, err := d.PruneLogins(ctx); n != 1 || err != nil {
		t.Errorf("pruned %d %v", n, err)
	}
	if n, err := d.DeleteLogins(ctx, a); n != 1 || err != nil {
		t.Errorf("deleted %d %v", n, err)
	}
}
