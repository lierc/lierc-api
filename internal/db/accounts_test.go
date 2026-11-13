package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lierc/lierc-api/internal/testenv"
)

func sp(s string) *string { return &s }

func TestAccountLookups(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	if err := d.InsertUser(ctx, "u1", "Alice", "a@x.test", "h1"); err != nil {
		t.Fatal(err)
	}
	a, ok, err := d.AccountByID(ctx, "u1")
	if err != nil || !ok || a.Username != "Alice" || a.Email != "a@x.test" || a.Verified == nil || *a.Verified {
		t.Fatalf("by id = %+v %v %v", a, ok, err)
	}
	if got := one[bool](t, d, `SELECT verify_sent IS NOT NULL AND registered IS NOT NULL FROM "user"`); !got {
		t.Error("verify_sent and registered not set")
	}
	if _, ok, err := d.AccountByID(ctx, "nope"); ok || err != nil {
		t.Errorf("unknown id: %v %v", ok, err)
	}
	for _, n := range []string{"ALICE", "A@X.test"} {
		rs, err := d.AccountsByLogin(ctx, n)
		if err != nil || len(rs) != 1 || rs[0].ID != "u1" || rs[0].PasswordHash == nil || *rs[0].PasswordHash != "h1" {
			t.Errorf("by login %q = %+v %v", n, rs, err)
		}
	}
	e, ok, err := d.AccountAuthByEmail(ctx, "A@x.test")
	if err != nil || !ok || e.ID != "u1" || e.PasswordHash == nil || *e.PasswordHash != "h1" {
		t.Errorf("by email = %+v %v %v", e, ok, err)
	}
	if _, ok, _ := d.AccountAuthByEmail(ctx, "no@x.test"); ok {
		t.Error("unknown email found")
	}
	i, ok, err := d.AccountAuthByID(ctx, "u1")
	if err != nil || !ok || i.Username != "Alice" || *i.PasswordHash != "h1" {
		t.Errorf("auth by id = %+v %v %v", i, ok, err)
	}
	if _, ok, _ := d.AccountAuthByID(ctx, "nope"); ok {
		t.Error("unknown id found")
	}

	err = d.InsertUser(ctx, "u2", "alice", "b@x.test", "h")
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23505" {
		t.Errorf("duplicate username: %v", err)
	}
}

func TestUserHasConnection(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	if has, err := d.UserHasConnection(ctx, a); has || err != nil {
		t.Fatalf("has = %v %v", has, err)
	}
	testenv.AddConnection(t, d, "c1", a, `{}`)
	if has, err := d.UserHasConnection(ctx, a); !has || err != nil {
		t.Fatalf("has = %v %v", has, err)
	}
}

func TestClaimVerifyResend(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	if err := d.InsertUser(ctx, "u1", "alice", "a@x.test", "h"); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.ClaimVerifyResend(ctx, "u1", 60); ok || err != nil {
		t.Errorf("claimed right after signup: %v %v", ok, err)
	}
	exec(t, d, `UPDATE "user" SET verify_sent = now() - interval '2 minutes'`)
	if ok, err := d.ClaimVerifyResend(ctx, "u1", 60); !ok || err != nil {
		t.Errorf("claim: %v %v", ok, err)
	}
	if ok, _ := d.ClaimVerifyResend(ctx, "u1", 60); ok {
		t.Error("claimed twice")
	}
	exec(t, d, `UPDATE "user" SET verify_sent = NULL, verified = true`)
	if ok, _ := d.ClaimVerifyResend(ctx, "u1", 60); ok {
		t.Error("claimed for a verified user")
	}
}

func TestReplacePasswordHashAndMarkVerified(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	exec(t, d, `INSERT INTO "user" (id, username, email, password_hash, verified) VALUES ('u1','alice','a@x.test','h1',NULL), ('u3','carol','c@x.test',NULL,true)`)
	if ok, err := d.ReplacePasswordHash(ctx, "u1", "h2", sp("stale")); ok || err != nil {
		t.Errorf("stale replace: %v %v", ok, err)
	}
	if ok, err := d.ReplacePasswordHash(ctx, "u1", "h2", nil); ok || err != nil {
		t.Errorf("replace expecting no hash, but there is one: %v %v", ok, err)
	}
	if ok, err := d.ReplacePasswordHash(ctx, "u1", "h2", sp("h1")); !ok || err != nil {
		t.Errorf("replace: %v %v", ok, err)
	}
	// an account with no password: only a replace expecting none applies
	if ok, err := d.ReplacePasswordHash(ctx, "u3", "h9", sp("h1")); ok || err != nil {
		t.Errorf("no-password replace expecting a hash: %v %v", ok, err)
	}
	if ok, err := d.ReplacePasswordHash(ctx, "u3", "h9", nil); !ok || err != nil {
		t.Errorf("no-password replace: %v %v", ok, err)
	}
	if got := one[string](t, d, `SELECT password_hash FROM "user" WHERE id='u1'`); got != "h2" {
		t.Errorf("stored %s", got)
	}
	// NULL verified becomes true but is not a first verification
	if first, err := d.MarkVerified(ctx, "u1"); first || err != nil {
		t.Errorf("null verify: %v %v", first, err)
	}
	if got := one[bool](t, d, `SELECT verified FROM "user"`); !got {
		t.Error("null not verified")
	}
	exec(t, d, `UPDATE "user" SET verified=false`)
	if first, err := d.MarkVerified(ctx, "u1"); !first || err != nil {
		t.Errorf("first verify: %v %v", first, err)
	}
	if first, _ := d.MarkVerified(ctx, "u1"); first {
		t.Error("second verify was first")
	}
}

func TestReplacePasswordAndVerify(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	exec(t, d, `INSERT INTO "user" (id, username, email, password_hash, verified) VALUES ('u1','alice','a@x.test','h1',false), ('u2','bob','b@x.test','h',true)`)
	for i, u := range []string{"u1", "u1", "u2"} {
		if err := d.InsertLogin(ctx, []byte{byte(i)}, u, "", ""); err != nil {
			t.Fatal(err)
		}
	}

	// a stale old hash changes nothing and ends no logins
	changed, first, err := d.ReplacePasswordAndVerify(ctx, "u1", "h2", sp("stale"))
	if changed || first || err != nil {
		t.Fatalf("stale = %v %v %v", changed, first, err)
	}
	if got := one[string](t, d, `SELECT password_hash || ' ' || verified::text || ' ' || (SELECT count(*) FROM login) FROM "user" WHERE id='u1'`); got != "h1 false 3" {
		t.Errorf("after stale: %s", got)
	}

	changed, first, err = d.ReplacePasswordAndVerify(ctx, "u1", "h2", sp("h1"))
	if !changed || !first || err != nil {
		t.Fatalf("replace = %v %v %v", changed, first, err)
	}
	if got := one[string](t, d, `SELECT password_hash || ' ' || verified::text FROM "user" WHERE id='u1'`); got != "h2 true" {
		t.Errorf("after replace: %s", got)
	}
	if got := one[string](t, d, `SELECT count(*) FILTER (WHERE "user"='u1') || ' ' || count(*) FILTER (WHERE "user"='u2') FROM login`); got != "0 1" {
		t.Errorf("logins left (u1 u2) = %s", got)
	}

	// already verified: changed, but not a first verification
	changed, first, err = d.ReplacePasswordAndVerify(ctx, "u2", "h3", sp("h"))
	if !changed || first || err != nil {
		t.Errorf("verified user = %v %v %v", changed, first, err)
	}
}

// The Perl API's legacy "password" column is gone: every account's password
// is a bcrypt password_hash (or none, until it's reset).
func TestNoLegacyPasswordColumn(t *testing.T) {
	d := testenv.Postgres(t)
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.columns WHERE table_name = 'user' AND column_name = 'password'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the user table still has a password column")
	}
}

// One account's email can be another's username (accounts from before
// usernames were restricted): a login by that name finds both.
func TestAccountsByLoginEmailIsAnothersUsername(t *testing.T) {
	d := testenv.Postgres(t)
	exec(t, d, `ALTER TABLE "user" DROP CONSTRAINT user_username_valid`)
	exec(t, d, `INSERT INTO "user" (id, username, email, password_hash, verified) VALUES
		('u1','alice','shared@x.test','h1',true), ('u2','Shared@X.test','bob@x.test','h2',true), ('u3','carol','c@x.test','h3',true)`)
	rs, err := d.AccountsByLogin(context.Background(), "SHARED@x.test")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rs {
		got[r.ID] = *r.PasswordHash
	}
	if len(got) != 2 || got["u1"] != "h1" || got["u2"] != "h2" {
		t.Errorf("AccountsByLogin = %v, want u1 and u2", got)
	}
}
