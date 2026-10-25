package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

var ctx = context.Background()

func TestUsers(t *testing.T) {
	d := testenv.Postgres(t)
	id := testenv.AddUser(t, d, "alice")
	if ok, err := d.UserExists(ctx, id); err != nil || !ok {
		t.Fatalf("UserExists = %v %v", ok, err)
	}
	if ok, _ := d.UserExists(ctx, "nope"); ok {
		t.Fatal("unexpected user")
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE "user" SET last_login='2000-01-01' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveLastLogin(ctx, id); err != nil {
		t.Fatal(err)
	}
	var recent bool
	if err := d.Pool.QueryRow(ctx, `SELECT last_login > NOW() - interval '1 minute' FROM "user" WHERE id=$1`, id).Scan(&recent); err != nil || !recent {
		t.Fatalf("last_login updated = %v %v", recent, err)
	}
}

func TestConnections(t *testing.T) {
	d := testenv.Postgres(t)
	testenv.AddConnection(t, d, "c1", "u1", `{"Host":"irc.example.com"}`)
	testenv.AddConnection(t, d, "c2", "u2", `{}`)
	conns, err := d.Connections(ctx, "u1")
	if err != nil || len(conns) != 1 || conns[0].ID != "c1" {
		t.Fatalf("Connections = %+v %v", conns, err)
	}
	var cfg map[string]string
	if err := json.Unmarshal(conns[0].Config, &cfg); err != nil || cfg["Host"] != "irc.example.com" {
		t.Fatalf("config = %s %v", conns[0].Config, err)
	}
	if conns, _ := d.Connections(ctx, "nobody"); conns == nil || len(conns) != 0 {
		t.Fatalf("empty Connections = %#v", conns)
	}

	cfgB, ok, err := d.LookupConfig(ctx, "c1")
	if err != nil || !ok || string(cfgB) != `{"Host":"irc.example.com"}` {
		t.Fatalf("LookupConfig = %s %v %v", cfgB, ok, err)
	}
	if _, ok, _ := d.LookupConfig(ctx, "zz"); ok {
		t.Fatal("LookupConfig found missing")
	}
	owner, ok, err := d.LookupOwner(ctx, "c1")
	if err != nil || !ok || owner != "u1" {
		t.Fatalf("LookupOwner = %q %v %v", owner, ok, err)
	}
	if _, ok, _ := d.LookupOwner(ctx, "zz"); ok {
		t.Fatal("LookupOwner found missing")
	}

	if err := d.UpdateConfig(ctx, "c1", []byte(`{"Host":"new"}`)); err != nil {
		t.Fatal(err)
	}
	if cfgB, _, _ = d.LookupConfig(ctx, "c1"); string(cfgB) != `{"Host":"new"}` {
		t.Fatalf("after update: %s", cfgB)
	}
}

func TestErrorsAreWrapped(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens here now
	d, err := db.Open(ctx, config.Config{DBHost: addr, DBUser: "u", DBName: "lierc"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var de *db.Error
	var ce *pgconn.ConnectError
	_, err = d.Pool.Exec(ctx, `SELECT 1`)
	if !errors.As(err, &de) || !errors.As(err, &ce) {
		t.Fatalf("Exec: %T %v", err, err)
	}
	if _, err := d.UserExists(ctx, "x"); !errors.As(err, &de) {
		t.Fatalf("QueryRow: %T %v", err, err)
	}

	live := testenv.Postgres(t)
	if _, err := live.Pool.Exec(ctx, `SELECT 1/0`); !errors.As(err, &de) {
		t.Fatalf("PgError: %T %v", err, err)
	}
	var n int
	err = live.Pool.QueryRow(ctx, `SELECT 1 WHERE false`).Scan(&n)
	if !errors.As(err, &de) || !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("ErrNoRows: %T %v", err, err)
	}
	rows, err := live.Pool.Query(ctx, `SELECT 'x'`)
	if err != nil {
		t.Fatal(err)
	}
	rows.Next()
	if err := rows.Scan(&n); !errors.As(err, &de) {
		t.Fatalf("rows.Scan: %T %v", err, err)
	}
	rows.Close()
	tctx, cancel := context.WithTimeout(ctx, time.Millisecond)
	defer cancel()
	if _, err := live.Pool.Exec(tctx, `SELECT pg_sleep(1)`); !errors.As(err, &de) {
		t.Fatalf("timeout: %T %v", err, err)
	}
}

func TestPoolMaxConns(t *testing.T) {
	for in, want := range map[int]int32{0: 20, 7: 7} {
		d, err := db.Open(ctx, config.Config{DBHost: "127.0.0.1:1", DBName: "lierc", DBMaxConns: in})
		if err != nil {
			t.Fatal(err)
		}
		if got := d.Pool.Config().MaxConns; got != want {
			t.Errorf("DBMaxConns %d: MaxConns = %d, want %d", in, got, want)
		}
		d.Close()
	}
}
