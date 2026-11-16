package db_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

const tableT = "CREATE TABLE t (a int)"

// lockT holds an exclusive lock on t from another session until the
// returned function is called (or the test ends).
func lockT(t *testing.T, d *db.DB) (release func()) {
	t.Helper()
	c, err := pgx.ConnectConfig(ctx, d.Pool.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "LOCK TABLE t IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	done := false
	release = func() {
		if !done {
			done = true
			tx.Rollback(ctx)
			c.Close(ctx)
		}
	}
	t.Cleanup(release)
	return release
}

func TestMigrateRetriesOnLockTimeout(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: files("0001_t.sql", tableT)}); err != nil {
		t.Fatal(err)
	}
	release := lockT(t, d)
	time.AfterFunc(500*time.Millisecond, release)

	var l logRec
	err := d.Migrate(ctx, db.MigrateOptions{
		FS:          files("0001_t.sql", tableT, "0002_add.sql", "ALTER TABLE t ADD COLUMN b int"),
		LockTimeout: 100 * time.Millisecond,
		Wait:        10 * time.Second,
		Logf:        l.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := recorded(t, d); !sameApplied(got, applied{1, "t"}, applied{2, "add"}) {
		t.Errorf("recorded %v", got)
	}
	if l.count("0002_add.sql", "lock") < 1 {
		t.Errorf("no retry logged: %q", l.lines)
	}
}

func TestMigrateGivesUpOnLock(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: files("0001_t.sql", tableT)}); err != nil {
		t.Fatal(err)
	}
	lockT(t, d)

	start := time.Now()
	err := d.Migrate(ctx, db.MigrateOptions{
		FS:          files("0001_t.sql", tableT, "0002_add.sql", "ALTER TABLE t ADD COLUMN b int"),
		LockTimeout: 100 * time.Millisecond,
		Wait:        time.Second,
	})
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("Migrate took %v", took)
	}
	var pgErr *pgconn.PgError
	if err == nil || !strings.Contains(err.Error(), "0002_add.sql") || !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("err = %v, want a lock timeout naming 0002_add.sql", err)
	}
	if got := recorded(t, d); !sameApplied(got, applied{1, "t"}) {
		t.Errorf("recorded %v", got)
	}
}

func TestMigrateRecordedNameMismatch(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: files("0001_initial.sql", tableA)}); err != nil {
		t.Fatal(err)
	}
	err := d.Migrate(ctx, db.MigrateOptions{FS: files("0001_other.sql", tableA, "0002_b.sql", "CREATE TABLE b (id int)")})
	if err == nil || !strings.Contains(err.Error(), "initial") || !strings.Contains(err.Error(), "other") {
		t.Fatalf("err = %v, want one naming initial and other", err)
	}
	if got := recorded(t, d); !sameApplied(got, applied{1, "initial"}) {
		t.Errorf("recorded %v", got)
	}
}

// A LockTimeout under a millisecond still times out: '0ms' would mean
// waiting forever.
func TestMigrateSubMillisecondLockTimeout(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: files("0001_t.sql", tableT)}); err != nil {
		t.Fatal(err)
	}
	lockT(t, d)
	err := d.Migrate(ctx, db.MigrateOptions{
		FS:          files("0001_t.sql", tableT, "0002_add.sql", "ALTER TABLE t ADD COLUMN b int"),
		LockTimeout: time.Microsecond,
		Wait:        500 * time.Millisecond,
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("err = %v, want a lock timeout", err)
	}
}
