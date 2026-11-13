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
	for _, c := range []struct {
		verified   any
		unverified bool
	}{
		{nil, false}, // grandfathered
		{false, true},
		{true, false},
	} {
		if _, err := d.Pool.Exec(ctx, `UPDATE "user" SET verified=$2 WHERE id=$1`, id, c.verified); err != nil {
			t.Fatal(err)
		}
		exists, unverified, err := d.UserState(ctx, id)
		if err != nil || !exists || unverified != c.unverified {
			t.Errorf("verified %v: UserState = %v %v %v", c.verified, exists, unverified, err)
		}
	}
	if exists, unverified, err := d.UserState(ctx, "nope"); err != nil || exists || unverified {
		t.Fatalf("missing user: UserState = %v %v %v", exists, unverified, err)
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
	if err != nil || !ok || !testenv.SameJSON(string(cfgB), `{"Host":"irc.example.com"}`) {
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

	if _, err := d.EditConfig(ctx, "c1", func([]byte) ([]byte, error) { return []byte(`{"Host":"new"}`), nil }); err != nil {
		t.Fatal(err)
	}
	if cfgB, _, _ = d.LookupConfig(ctx, "c1"); !testenv.SameJSON(string(cfgB), `{"Host":"new"}`) {
		t.Fatalf("after update: %s", cfgB)
	}
}

// The stream server's save of the channel list marks the connection
// updated; created is left as it was.
func TestEditConfigTouchesUpdated(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	testenv.AddConnection(t, d, "c1", "u1", `{}`)

	if _, err := d.EditConfig(ctx, "c1", func([]byte) ([]byte, error) { return []byte(`{"Channels":["#a"]}`), nil }); err != nil {
		t.Fatal(err)
	}
	// Compared in SQL: the columns are timestamp without time zone.
	var createdNull, updatedNow bool
	if err := d.Pool.QueryRow(ctx, `SELECT created IS NULL, updated > NOW() - interval '1 minute'
		FROM connection WHERE id='c1'`).Scan(&createdNull, &updatedNow); err != nil {
		t.Fatal(err)
	}
	if !createdNull {
		t.Error("created was set, want NULL")
	}
	if !updatedNow {
		t.Error("updated is not now")
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
	if _, _, err := d.UserState(ctx, "x"); !errors.As(err, &de) {
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

func TestLatestMessageID(t *testing.T) {
	d := testenv.Postgres(t)
	if id, err := d.LatestMessageID(ctx, "alice"); err != nil || id != 0 {
		t.Fatalf("no messages: LatestMessageID = %d %v", id, err)
	}
	testenv.AddLog(t, d, 5, "ca", "alice")
	testenv.AddLog(t, d, 9, "ca", "alice")
	testenv.AddLog(t, d, 12, "cb", "bob")
	testenv.AddLog(t, d, 15, "cc", "carol")
	testenv.AddLog(t, d, 20, "cc", "carol")
	// carol's newest is a highlight, alice's highlight is older than her newest.
	if _, err := d.Pool.Exec(ctx, `UPDATE log SET highlight = true WHERE id IN (5, 20)`); err != nil {
		t.Fatal(err)
	}
	for user, want := range map[string]int64{"alice": 9, "bob": 12, "carol": 20, "nobody": 0} {
		if id, err := d.LatestMessageID(ctx, user); err != nil || id != want {
			t.Errorf("LatestMessageID(%q) = %d %v, want %d", user, id, err, want)
		}
	}
}

func TestLookupLiercd(t *testing.T) {
	d := testenv.Postgres(t)
	testenv.AddConnection(t, d, "c1", "u1", `{}`)
	testenv.AddConnection(t, d, "c2", "u1", `{}`)
	if _, err := d.Pool.Exec(ctx, `UPDATE connection SET liercd='next' WHERE id='c2'`); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"c1": "", "c2": "next", "nope": ""} {
		if got, err := d.LookupLiercd(ctx, id); err != nil || got != want {
			t.Errorf("LookupLiercd(%q) = %q %v, want %q", id, got, err, want)
		}
	}
}

func TestUserExists(t *testing.T) {
	d := testenv.Postgres(t)
	id := testenv.AddUser(t, d, "alice")
	for _, c := range []struct {
		id   string
		want bool
	}{{id, true}, {"nobody", false}} {
		got, err := d.UserExists(ctx, c.id)
		if err != nil || got != c.want {
			t.Errorf("UserExists(%q) = %v, %v; want %v", c.id, got, err, c.want)
		}
	}
}

func TestNotifyHighlights(t *testing.T) {
	d := testenv.Postgres(t)
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := d.Pool.Acquire(lctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(lctx, "LISTEN highlights"); err != nil {
		t.Fatal(err)
	}
	if err := d.NotifyHighlights(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := conn.Conn().WaitForNotification(lctx); err != nil || n.Channel != "highlights" {
		t.Errorf("notification = %+v, %v", n, err)
	}
}

func savedConfig(t *testing.T, d *db.DB, id string) string {
	t.Helper()
	b, _, err := d.LookupConfig(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// liercd's changes replace only the fields they name.
func TestMergeConfig(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	testenv.AddConnection(t, d, "c1", "u1", `{"Host":"h","Channels":["#a"]}`)
	testenv.AddConnection(t, d, "c2", "u1", `[]`)

	if err := d.MergeConfig(ctx, "c1", []byte(`{"Channels":["#b"]}`)); err != nil {
		t.Fatal(err)
	}
	if got := savedConfig(t, d, "c1"); !testenv.SameJSON(got, `{"Host":"h","Channels":["#b"]}`) {
		t.Errorf("merged %s", got)
	}
	if err := d.MergeConfig(ctx, "zz", []byte(`{"Nick":"x"}`)); err != nil {
		t.Errorf("missing connection: %v", err)
	}
	if err := d.MergeConfig(ctx, "c2", []byte(`{"Nick":"x"}`)); err != nil {
		t.Errorf("non-object config: %v", err)
	}
	if got := savedConfig(t, d, "c2"); !testenv.SameJSON(got, `[]`) {
		t.Errorf("non-object config changed to %s", got)
	}
	if err := d.MergeConfig(ctx, "c1", []byte(`[1]`)); err != nil {
		t.Errorf("non-object fields: %v", err)
	}
	if got := savedConfig(t, d, "c1"); !testenv.SameJSON(got, `{"Host":"h","Channels":["#b"]}`) {
		t.Errorf("non-object fields changed it to %s", got)
	}
}

// EditConfig hands the edit the saved config and stores what it returns.
func TestEditConfig(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	testenv.AddConnection(t, d, "c1", "u1", `{"Host":"h"}`)

	var saw string
	found, err := d.EditConfig(ctx, "c1", func(saved []byte) ([]byte, error) {
		saw = string(saved)
		return []byte(`{"Host":"new"}`), nil
	})
	if err != nil || !found {
		t.Fatalf("EditConfig = %v, %v", found, err)
	}
	if !testenv.SameJSON(saw, `{"Host":"h"}`) {
		t.Errorf("edit saw %s", saw)
	}
	if got := savedConfig(t, d, "c1"); !testenv.SameJSON(got, `{"Host":"new"}`) {
		t.Errorf("saved %s", got)
	}
	var updatedNow bool
	if err := d.Pool.QueryRow(ctx, `SELECT updated > NOW() - interval '1 minute' FROM connection WHERE id='c1'`).Scan(&updatedNow); err != nil || !updatedNow {
		t.Errorf("updated is not now (%v)", err)
	}

	called := false
	found, err = d.EditConfig(ctx, "zz", func([]byte) ([]byte, error) { called = true; return nil, nil })
	if err != nil || found || called {
		t.Errorf("missing connection: found %v, err %v, edit called %v", found, err, called)
	}
}

// An edit that fails changes nothing.
func TestEditConfigRollsBack(t *testing.T) {
	d := testenv.Postgres(t)
	testenv.AddConnection(t, d, "c1", "u1", `{"Host":"h"}`)
	no := errors.New("no")
	found, err := d.EditConfig(context.Background(), "c1", func([]byte) ([]byte, error) { return []byte(`{"Host":"x"}`), no })
	if !errors.Is(err, no) || !found {
		t.Fatalf("EditConfig = %v, %v; want true, the edit's error", found, err)
	}
	if got := savedConfig(t, d, "c1"); !testenv.SameJSON(got, `{"Host":"h"}`) {
		t.Errorf("saved %s after a failed edit", got)
	}
}

// A merge made during an edit waits for it, then lands on top: the
// edit can't overwrite a channel liercd joined meanwhile.
func TestEditConfigBlocksMerge(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	testenv.AddConnection(t, d, "c1", "u1", `{"Host":"h","Channels":["#a"]}`)

	inside, release := make(chan struct{}), make(chan struct{})
	edited := make(chan error, 1)
	go func() {
		_, err := d.EditConfig(ctx, "c1", func([]byte) ([]byte, error) {
			close(inside)
			<-release
			return []byte(`{"Host":"edited","Channels":["#a"]}`), nil
		})
		edited <- err
	}()
	<-inside
	merged := make(chan error, 1)
	go func() { merged <- d.MergeConfig(ctx, "c1", []byte(`{"Channels":["#new"]}`)) }()
	select {
	case err := <-merged:
		close(release)
		<-edited
		t.Fatalf("merge didn't wait for the edit (%v)", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-edited; err != nil {
		t.Fatal(err)
	}
	if err := <-merged; err != nil {
		t.Fatal(err)
	}
	if got := savedConfig(t, d, "c1"); !testenv.SameJSON(got, `{"Host":"edited","Channels":["#new"]}`) {
		t.Errorf("saved %s", got)
	}
}
