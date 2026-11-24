package db_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

// logRec collects Logf lines.
type logRec struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRec) Logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

// count is the number of lines containing every one of subs.
func (l *logRec) count(subs ...string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
outer:
	for _, line := range l.lines {
		for _, s := range subs {
			if !strings.Contains(line, s) {
				continue outer
			}
		}
		n++
	}
	return n
}

func files(kv ...string) fstest.MapFS {
	fs := fstest.MapFS{}
	for i := 0; i < len(kv); i += 2 {
		fs[kv[i]] = &fstest.MapFile{Data: []byte(kv[i+1])}
	}
	return fs
}

type applied struct {
	version int
	name    string
}

func recorded(t *testing.T, d *db.DB) []applied {
	t.Helper()
	rows, err := d.Pool.Query(ctx, "SELECT version, name FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	var out []applied
	for rows.Next() {
		var a applied
		if err := rows.Scan(&a.version, &a.name); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func tableExists(t *testing.T, d *db.DB, name string) bool {
	t.Helper()
	var ok bool
	if err := d.Pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func sameApplied(got []applied, want ...applied) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

const tableA = "CREATE TABLE a (id int PRIMARY KEY)"

func TestMigrateAppliesInOrder(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	var l logRec
	err := d.Migrate(ctx, db.MigrateOptions{
		FS:   files("0002_b.sql", "CREATE TABLE b (id int REFERENCES a)", "0001_a.sql", tableA),
		Logf: l.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := recorded(t, d); !sameApplied(got, applied{1, "a"}, applied{2, "b"}) {
		t.Errorf("recorded %v", got)
	}
	if l.count("applying", "0001_a") != 1 || l.count("applying", "0002_b") != 1 {
		t.Errorf("log: %q", l.lines)
	}
}

func TestMigrateSecondRunAppliesNothing(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	fs := files("0001_a.sql", tableA, "0002_b.sql", "CREATE TABLE b (id int)")
	if err := d.Migrate(ctx, db.MigrateOptions{FS: fs}); err != nil {
		t.Fatal(err)
	}
	var l logRec
	if err := d.Migrate(ctx, db.MigrateOptions{FS: fs, Logf: l.Logf}); err != nil {
		t.Fatal(err)
	}
	if n := l.count("applying"); n != 0 {
		t.Errorf("second run applied: %q", l.lines)
	}
	if got := recorded(t, d); len(got) != 2 {
		t.Errorf("recorded %v", got)
	}
}

func TestMigrateFailureRollsBack(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	err := d.Migrate(ctx, db.MigrateOptions{FS: files(
		"0001_a.sql", tableA,
		"0002_bad.sql", "CREATE TABLE c (id int); SELECT nosuchcolumn FROM a;",
	)})
	if err == nil || !strings.Contains(err.Error(), "0002_bad") {
		t.Fatalf("err = %v, want one naming 0002_bad", err)
	}
	if tableExists(t, d, "c") {
		t.Error("table c survived the failed migration")
	}
	if got := recorded(t, d); !sameApplied(got, applied{1, "a"}) {
		t.Errorf("recorded %v", got)
	}
}

func TestMigrateConcurrent(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	fs := files("0001_a.sql", "CREATE TABLE a (id int); SELECT pg_sleep(0.3)")
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- d.Migrate(ctx, db.MigrateOptions{FS: fs}) }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	if got := recorded(t, d); !sameApplied(got, applied{1, "a"}) {
		t.Errorf("recorded %v", got)
	}
}

func TestMigrateReleasesLock(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: files("0001_a.sql", tableA)}); err != nil {
		t.Fatal(err)
	}
	c, err := pgx.ConnectConfig(ctx, d.Pool.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var got bool
	if err := c.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", db.MigrateLockKey).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("the migration lock is still held after Migrate")
	}
}

func TestMigrateUnknownRecordedVersion(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if _, err := d.Pool.Exec(ctx, `CREATE TABLE schema_migrations (
		version int PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now());
		INSERT INTO schema_migrations (version, name) VALUES (9, 'future')`); err != nil {
		t.Fatal(err)
	}
	var l logRec
	if err := d.Migrate(ctx, db.MigrateOptions{FS: files("0001_a.sql", tableA), Logf: l.Logf}); err != nil {
		t.Fatal(err)
	}
	if !tableExists(t, d, "a") {
		t.Error("0001_a wasn't applied")
	}
	if l.count("9", "future") == 0 {
		t.Errorf("no log line about version 9: %q", l.lines)
	}
}

func TestMigrateBadFileNames(t *testing.T) {
	for _, c := range []struct {
		fs   fstest.MapFS
		name string
	}{
		{files("0001_a.sql", tableA, "0002_add-col.sql", "SELECT 1"), "0002_add-col.sql"},
		{files("0001_a.sql", tableA, "2_x.sql", "SELECT 1"), "2_x.sql"},
		{files("0001_a.sql", tableA, "README", "notes"), "README"},
		{files("0001_a.sql", tableA, "0002_a.sql", "SELECT 1", "0002_b.sql", "SELECT 1"), "0002_"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := testenv.EmptyPostgres(t)
			err := d.Migrate(ctx, db.MigrateOptions{FS: c.fs})
			if err == nil || !strings.Contains(err.Error(), c.name) {
				t.Fatalf("err = %v, want one naming %s", err, c.name)
			}
			if tableExists(t, d, "a") || tableExists(t, d, "schema_migrations") {
				t.Error("something was applied before the bad file was refused")
			}
		})
	}
}

func TestMigrateRefusesTransactionControl(t *testing.T) {
	for _, stmt := range []string{"COMMIT;", "  begin;", "ROLLBACK;", "start transaction;", "End;"} {
		t.Run(stmt, func(t *testing.T) {
			d := testenv.EmptyPostgres(t)
			err := d.Migrate(ctx, db.MigrateOptions{FS: files("0001_a.sql", "CREATE TABLE a (id int);\n"+stmt+"\n")})
			if err == nil || !strings.Contains(err.Error(), "0001_a") {
				t.Fatalf("err = %v, want one naming 0001_a", err)
			}
			if tableExists(t, d, "a") {
				t.Error("table a was created")
			}
		})
	}
}

func TestMigrateRewrite(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	err := d.Migrate(ctx, db.MigrateOptions{
		FS:      files("0001_a.sql", "CREATE TABLE XXX (id int)"),
		Rewrite: func(s string) string { return strings.ReplaceAll(s, "XXX", "a") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !tableExists(t, d, "a") {
		t.Error("the rewritten SQL didn't run")
	}
}

// closedPortDB is a DB pointing at a local port nothing listens on.
func closedPortDB(t *testing.T) *db.DB {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	d, err := db.Open(ctx, config.Config{DBHost: addr, DBUser: "x", DBPass: "x", DBName: "x"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

func TestMigrateWaitsForDatabase(t *testing.T) {
	d := closedPortDB(t)
	var l logRec
	start := time.Now()
	err := d.Migrate(ctx, db.MigrateOptions{FS: files("0001_a.sql", tableA), Wait: 400 * time.Millisecond, Logf: l.Logf})
	if err == nil {
		t.Fatal("no error from a database that isn't there")
	}
	if took := time.Since(start); took < 400*time.Millisecond {
		t.Errorf("gave up after %v, want >= 400ms", took)
	}
	if n := len(l.lines); n < 2 {
		t.Errorf("want >= 2 retry lines, got %q", l.lines)
	}
}

func TestMigrateWaitStopsOnCancel(t *testing.T) {
	d := closedPortDB(t)
	c, cancel := context.WithCancel(ctx)
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	err := d.Migrate(c, db.MigrateOptions{FS: files("0001_a.sql", tableA), Wait: time.Minute})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("took %v to notice the cancel", took)
	}
}

// otherLogin is a DB on the tests' server with one login detail changed.
func otherLogin(t *testing.T, change func(*config.Config)) *db.DB {
	t.Helper()
	cc := testenv.EmptyPostgres(t).Pool.Config().ConnConfig
	cfg := config.Config{
		DBHost: net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port))),
		DBUser: cc.User, DBPass: cc.Password, DBName: cc.Database,
	}
	change(&cfg)
	d, err := db.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

func failsFast(t *testing.T, d *db.DB) {
	t.Helper()
	start := time.Now()
	err := d.Migrate(ctx, db.MigrateOptions{FS: files("0001_a.sql", tableA), Wait: time.Minute})
	if err == nil {
		t.Fatal("no error")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("took %v; this error shouldn't be retried (%v)", took, err)
	}
}

func TestMigrateFailsFastOnBadPassword(t *testing.T) {
	failsFast(t, otherLogin(t, func(c *config.Config) { c.DBUser, c.DBPass = "nosuchrole", "wrong" }))
}

func TestMigrateFailsFastOnMissingDatabase(t *testing.T) {
	failsFast(t, otherLogin(t, func(c *config.Config) { c.DBName = "nosuch" }))
}

func TestEmbeddedMigrationsApply(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	err := d.Migrate(ctx, db.MigrateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !tableExists(t, d, "log_archives") {
		t.Error("no log_archives table")
	}
	if got, want := recorded(t, d), embeddedMigrations(t); !sameApplied(got, want...) {
		t.Errorf("recorded %v, want %v", got, want)
	}
}

// BEGIN and END inside a dollar-quoted function body aren't transaction
// control.
func TestMigrateAllowsFunctionBodies(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	err := d.Migrate(ctx, db.MigrateOptions{FS: files("0001_f.sql", `CREATE FUNCTION f() RETURNS int AS $body$
BEGIN
  RETURN 1;
END;
$body$ LANGUAGE plpgsql;
DO $$
BEGIN
  PERFORM f();
END
$$;`)})
	if err != nil {
		t.Fatal(err)
	}
}

// WaitMigrated waits until every embedded migration is applied: lierc
// ingest calls it, so a new ingest never writes to a schema lierc-api
// hasn't migrated yet.
func TestWaitMigrated(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: migrationsUpTo(t, 9)}); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if err := d.WaitMigrated(short, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitMigrated before 0010 = %v, want the deadline", err)
	}
	done := make(chan error, 1)
	go func() {
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		done <- d.WaitMigrated(wctx, t.Logf)
	}()
	time.Sleep(100 * time.Millisecond)
	if err := d.Migrate(ctx, db.MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("WaitMigrated = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("WaitMigrated still waiting 3s after the migrations")
	}
}
