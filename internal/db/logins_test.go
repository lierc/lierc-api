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

// Logins lists a user's live logins, most recently used first (ties by
// newest id), leaving out those unused for 7 days; DeleteLoginByHash ends
// only the login with that hash.
func TestLoginsOrderExpiryAndDeleteByHash(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	for i := 1; i <= 4; i++ {
		if err := d.InsertLogin(ctx, []byte{byte(i)}, a, "agent", ""); err != nil {
			t.Fatal(err)
		}
	}
	exec(t, d, `UPDATE login SET last_used = now() - interval '1 hour' WHERE token_hash = '\x01'`)
	exec(t, d, `UPDATE login SET last_used = now() - interval '10 minutes' WHERE token_hash IN ('\x02', '\x03')`)
	exec(t, d, `UPDATE login SET last_used = now() - interval '8 days' WHERE token_hash = '\x04'`)
	idOf := func(h byte) int64 {
		return one[int64](t, d, `SELECT id FROM login WHERE token_hash = $1`, []byte{h})
	}
	infos, err := d.Logins(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, l := range infos {
		got = append(got, l.ID)
	}
	want := []int64{idOf(3), idOf(2), idOf(1)} // 2 and 3 tie: newest id first; 4 expired
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("Logins = %v, want %v", got, want)
	}

	if err := d.DeleteLoginByHash(ctx, []byte{2}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := d.LoginByHash(ctx, []byte{2}); ok || err != nil {
		t.Errorf("deleted login still found: %v %v", ok, err)
	}
	for _, h := range []byte{1, 3} {
		if _, ok, err := d.LoginByHash(ctx, []byte{h}); !ok || err != nil {
			t.Errorf("login %d gone after deleting another: %v %v", h, ok, err)
		}
	}
}
