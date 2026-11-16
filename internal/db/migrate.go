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
// row each. The production baseline file (baseline.sql) creates the same.
const schemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    int PRIMARY KEY,
  name       text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
)`

const (
	defaultMigrateWait = 60 * time.Second
	firstRetryDelay    = 250 * time.Millisecond
	maxRetryDelay      = 5 * time.Second
)

type MigrateOptions struct {
	// FS holds the migration files at its root; nil means the embedded ones.
	FS fs.FS
	// Rewrite, if set, is applied to each migration's SQL before it runs.
	Rewrite func(sql string) string
	// Wait is how long to keep retrying a database that isn't reachable
	// yet; 0 means a minute.
	Wait time.Duration
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
// Recorded versions it doesn't have are logged and otherwise ignored.
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
	c, err := d.waitForConn(ctx, wait, logf)
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
	done := map[int]bool{}
	rows, err := c.Query(ctx, "SELECT version, name FROM schema_migrations ORDER BY version")
	if err != nil {
		return wrap(err)
	}
	known := map[int]bool{}
	for _, m := range ms {
		known[m.version] = true
	}
	for rows.Next() {
		var v int
		var name string
		if err := rows.Scan(&v, &name); err != nil {
			rows.Close()
			return wrap(err)
		}
		done[v] = true
		if !known[v] {
			logf("the database has migration %d (%s), which this lierc-api doesn't; carrying on", v, name)
		}
	}
	if err := rows.Err(); err != nil {
		return wrap(err)
	}

	for _, m := range ms {
		if done[m.version] {
			continue
		}
		sql := m.sql
		if opts.Rewrite != nil {
			sql = opts.Rewrite(sql)
		}
		logf("applying migration %s", m.file)
		start := time.Now()
		if err := apply(ctx, c, m, sql); err != nil {
			return wrap(fmt.Errorf("migration %s: %w", m.file, err))
		}
		logf("applied migration %s in %v", m.file, time.Since(start).Round(time.Millisecond))
	}
	return nil
}

// apply runs one migration and records it, in one transaction.
func apply(ctx context.Context, c *pgxpool.Conn, m migration, sql string) error {
	tx, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
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

// waitForConn acquires a connection, retrying for up to wait while the
// database isn't reachable yet. Other errors (a wrong password, a missing
// database) return at once.
func (d *DB) waitForConn(ctx context.Context, wait time.Duration, logf func(string, ...any)) (*pgxpool.Conn, error) {
	deadline := time.Now().Add(wait)
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
