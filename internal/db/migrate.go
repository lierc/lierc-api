package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The schema: migrations/NNNN_name.sql, applied in order by Migrate. A
// migration is never edited once applied anywhere; a change is a new file.
//
//go:embed migrations/*.sql
var embedded embed.FS

// migrateLockKey is the advisory lock Migrate holds while it runs ("lierc").
const migrateLockKey int64 = 0x6c69657263

// schemaMigrationsDDL creates the table recording applied migrations, one
// row each.
const schemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    int PRIMARY KEY,
  name       text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
)`

const (
	defaultMigrateWait = 60 * time.Second
	defaultLockTimeout = 5 * time.Second
	firstRetryDelay    = 250 * time.Millisecond
	maxRetryDelay      = 5 * time.Second
)

type MigrateOptions struct {
	// FS holds the migration files at its root; nil means the embedded ones.
	FS fs.FS
	// Rewrite, if set, is applied to each migration's SQL before it runs.
	Rewrite func(sql string) string
	// Wait is how long to keep retrying, from the start of Migrate: a
	// database that isn't reachable yet, and migrations that couldn't get
	// a lock; 0 means a minute.
	Wait time.Duration
	// LockTimeout is how long a migration waits for a lock before it is
	// rolled back and retried; 0 means 5s.
	LockTimeout time.Duration
	// Logf gets a line per retry, per applied migration and per recorded
	// migration this binary doesn't have; nil discards them.
	Logf func(format string, args ...any)
}

type migration struct {
	version    int
	name, file string
	sql        string
}

var (
	migrationFile = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)
	// Statements that would end Migrate's transaction early.
	txControl   = regexp.MustCompile(`(?im)^\s*(begin|commit|rollback|end|abort|start\s+transaction)\b`)
	dollarQuote = regexp.MustCompile(`\$[A-Za-z_][A-Za-z_0-9]*\$|\$\$`)
)

// Migrate applies the migrations the database hasn't had, each in its own
// transaction together with its schema_migrations row, in version order. It
// waits up to opts.Wait for a database that isn't reachable yet, and holds
// an advisory lock throughout so concurrent runs apply each migration once.
// A migration that waits longer than opts.LockTimeout for a lock, or is
// chosen as a deadlock victim, is rolled back and retried within the same
// opts.Wait. Recorded versions it doesn't have are logged and otherwise
// ignored; a recorded version it has under another name is an error.
func (d *DB) Migrate(ctx context.Context, opts MigrateOptions) error {
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	fsys := opts.FS
	if fsys == nil {
		sub, err := fs.Sub(embedded, "migrations")
		if err != nil {
			return err
		}
		fsys = sub
	}
	ms, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	wait := opts.Wait
	if wait <= 0 {
		wait = defaultMigrateWait
	}
	lockTimeout := opts.LockTimeout
	if lockTimeout <= 0 {
		lockTimeout = defaultLockTimeout
	}
	deadline := time.Now().Add(wait)
	c, err := d.waitForConn(ctx, deadline, logf)
	if err != nil {
		return err
	}
	defer c.Release()

	if _, err := c.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey); err != nil {
		return wrap(fmt.Errorf("taking the migration lock: %w", err))
	}
	// Unlock before Release (defers run last-in first-out): a pooled
	// connection must not keep holding the lock. If the unlock fails, the
	// connection is closed, which releases the lock, and the pool drops it.
	defer func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := c.Exec(uctx, "SELECT pg_advisory_unlock($1)", migrateLockKey); err != nil {
			c.Conn().Close(uctx)
		}
	}()

	if _, err := c.Exec(ctx, schemaMigrationsDDL); err != nil {
		return wrap(fmt.Errorf("creating schema_migrations: %w", err))
	}
	done := map[int]string{}
	rows, err := c.Query(ctx, "SELECT version, name FROM schema_migrations ORDER BY version")
	if err != nil {
		return wrap(err)
	}
	known := map[int]string{}
	for _, m := range ms {
		known[m.version] = m.name
	}
	for rows.Next() {
		var v int
		var name string
		if err := rows.Scan(&v, &name); err != nil {
			rows.Close()
			return wrap(err)
		}
		done[v] = name
		if mine, ok := known[v]; !ok {
			logf("the database has migration %d (%s), which this lierc-api doesn't; carrying on", v, name)
		} else if mine != name {
			rows.Close()
			return fmt.Errorf("migration %d is recorded as %q but this lierc-api has %q", v, name, mine)
		}
	}
	if err := rows.Err(); err != nil {
		return wrap(err)
	}

	for _, m := range ms {
		if _, ok := done[m.version]; ok {
			continue
		}
		sql := m.sql
		if opts.Rewrite != nil {
			sql = opts.Rewrite(sql)
		}
		logf("applying migration %s", m.file)
		start := time.Now()
		delay := firstRetryDelay
		for {
			err := apply(ctx, c, m, sql, lockTimeout)
			if err == nil {
				break
			}
			left := time.Until(deadline)
			if !lockConflict(err) || left <= 0 || ctx.Err() != nil {
				return wrap(fmt.Errorf("migration %s: %w", m.file, err))
			}
			pause := min(delay, left)
			logf("migration %s couldn't get a lock (%v); retrying in %v", m.file, err, pause.Round(time.Millisecond))
			select {
			case <-ctx.Done():
				return fmt.Errorf("migration %s: %w", m.file, ctx.Err())
			case <-time.After(pause):
			}
			delay = min(delay*2, maxRetryDelay)
		}
		logf("applied migration %s in %v", m.file, time.Since(start).Round(time.Millisecond))
	}
	return nil
}

// apply runs one migration and records it, in one transaction that waits
// at most lockTimeout for each lock.
func apply(ctx context.Context, c *pgxpool.Conn, m migration, sql string, lockTimeout time.Duration) error {
	tx, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	// At least 1ms: lock_timeout 0 means no timeout at all.
	ms := max(lockTimeout.Milliseconds(), 1)
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL lock_timeout = '%dms'", ms)); err != nil {
		return err
	}
	// No arguments: the simple protocol, so the file may hold many statements.
	if _, err := tx.Exec(ctx, sql); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", m.version, m.name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// loadMigrations reads and checks every file in fsys before anything runs:
// each must be named NNNN_name.sql with a positive, unique version, and
// hold no transaction control statements.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("reading the migrations: %w", err)
	}
	byVersion := map[int]string{}
	var ms []migration
	for _, e := range entries {
		f := e.Name()
		match := migrationFile.FindStringSubmatch(f)
		if e.IsDir() || match == nil {
			return nil, fmt.Errorf("migration file %q: want a name like 0001_name.sql (lower case, digits, underscores)", f)
		}
		v, _ := strconv.Atoi(match[1])
		if v == 0 {
			return nil, fmt.Errorf("migration file %q: versions start at 0001", f)
		}
		if other, ok := byVersion[v]; ok {
			return nil, fmt.Errorf("migration files %q and %q have the same version", other, f)
		}
		byVersion[v] = f
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("migration file %q: %w", f, err)
		}
		if stmt := txControl.FindString(withoutDollarQuotes(string(b))); stmt != "" {
			return nil, fmt.Errorf("migration file %q: %q would end its transaction; each migration already runs in one",
				f, strings.TrimSpace(stmt))
		}
		ms = append(ms, migration{version: v, name: match[2], file: f, sql: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}

// withoutDollarQuotes blanks out dollar-quoted strings ($$...$$,
// $tag$...$tag$), such as function bodies, whose BEGIN and END aren't
// transaction control. An unterminated quote blanks the rest.
func withoutDollarQuotes(s string) string {
	var b strings.Builder
	for {
		loc := dollarQuote.FindStringIndex(s)
		if loc == nil {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:loc[0]])
		tag := s[loc[0]:loc[1]]
		rest := s[loc[1]:]
		end := strings.Index(rest, tag)
		if end < 0 {
			return b.String()
		}
		b.WriteString("\n")
		s = rest[end+len(tag):]
	}
}

// waitForConn acquires a connection, retrying until deadline while the
// database isn't reachable yet. Other errors (a wrong password, a missing
// database) return at once.
func (d *DB) waitForConn(ctx context.Context, deadline time.Time, logf func(string, ...any)) (*pgxpool.Conn, error) {
	delay := firstRetryDelay
	for {
		c, err := d.Pool.Acquire(ctx)
		if err == nil {
			return c, nil
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("waiting for the database: %w", ctx.Err())
		}
		left := time.Until(deadline)
		if !retryable(err) || left <= 0 {
			return nil, err
		}
		pause := min(delay, left)
		logf("database not reachable yet (%v); retrying in %v", err, pause.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for the database: %w", ctx.Err())
		case <-time.After(pause):
		}
		delay = min(delay*2, maxRetryDelay)
	}
}

// retryable reports whether err means the database isn't reachable yet:
// nothing listening, the connection dropped or timed out, or the server
// starting up or shutting down.
func retryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "57P03" || pgErr.Code == "57P01" // cannot_connect_now, admin_shutdown
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// lockConflict reports whether err means the migration lost out on a lock:
// it waited longer than lock_timeout, or was a deadlock's victim.
func lockConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "55P03" || pgErr.Code == "40P01") // lock_not_available, deadlock_detected
}
