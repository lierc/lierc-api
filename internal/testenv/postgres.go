// Package testenv provides in-process Postgres, nsqd and liercd fakes for tests.
package testenv

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	embeddedpg "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/passwords"
)

const templateDB = "lierc_template"

// pgVersion is the PostgreSQL the tests run (production runs 18; the
// library's newest is 17).
const pgVersion = embeddedpg.V17

var (
	pgOnce   sync.Once
	pgErr    error
	pgServer *embeddedpg.EmbeddedPostgres
	pgPort   uint32
	pgDir    string
	dbSeq    atomic.Int64
)

// Main is for TestMain: it runs the tests, then stops the embedded Postgres
// (started lazily by the first Postgres call) and removes its files and the
// nsqd binary NSQD builds.
func Main(m *testing.M) {
	passwords.UseTestCost()
	code := m.Run()
	if pgServer != nil {
		pgServer.Stop()
	}
	if pgDir != "" {
		os.RemoveAll(pgDir)
	}
	if nsqdDir != "" {
		os.RemoveAll(nsqdDir)
	}
	os.Exit(code)
}

func startPostgres() error {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	pgPort = uint32(l.Addr().(*net.TCPAddr).Port)
	l.Close()

	pgDir, err = os.MkdirTemp("", "lierc-pg-")
	if err != nil {
		return err
	}
	bin, release, err := pgBinaries(pgBinariesRoot(), string(pgVersion))
	if err != nil {
		return err
	}
	cfg := embeddedpg.DefaultConfig().
		Version(pgVersion).
		Port(pgPort).
		RuntimePath(filepath.Join(pgDir, "run")).
		DataPath(filepath.Join(pgDir, "data")).
		BinariesPath(bin).
		StartTimeout(120 * time.Second).
		Logger(nil)
	pgServer = embeddedpg.NewDatabase(cfg)
	err = pgServer.Start()
	release(err == nil)
	if err != nil {
		pgServer = nil
		return err
	}
	return createTemplate()
}

func adminConn(ctx context.Context) (*pgx.Conn, error) {
	return pgx.Connect(ctx, fmt.Sprintf(
		"postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", pgPort))
}

// createTemplate builds the template database every test database is
// cloned from, by running the migrations. The embedded Postgres is built
// without lz4, so the migrations' "compression lz4" is left out; the
// compression method changes no results.
func createTemplate() error {
	ctx := context.Background()
	c, err := adminConn(ctx)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, "CREATE DATABASE "+templateDB); err != nil {
		return err
	}
	d, err := db.Open(ctx, config.Config{
		DBHost: fmt.Sprintf("127.0.0.1:%d", pgPort),
		DBUser: "postgres", DBPass: "postgres", DBName: templateDB,
	})
	if err != nil {
		return err
	}
	// Closed before any clone: CREATE DATABASE ... TEMPLATE needs the
	// template to have no connections.
	defer d.Close()
	return d.Migrate(ctx, db.MigrateOptions{
		Rewrite: func(sql string) string { return strings.ReplaceAll(sql, " compression lz4", "") },
	})
}

// Postgres returns a DB backed by a fresh database cloned from the
// migrated template.
func Postgres(t testing.TB) *db.DB {
	t.Helper()
	return newDatabase(t, " TEMPLATE "+templateDB)
}

// EmptyPostgres returns a DB backed by a fresh database with no tables.
func EmptyPostgres(t testing.TB) *db.DB {
	t.Helper()
	return newDatabase(t, "")
}

// newDatabase creates a database with CREATE DATABASE's extra clause opts.
func newDatabase(t testing.TB, opts string) *db.DB {
	t.Helper()
	pgOnce.Do(func() { pgErr = startPostgres() })
	if pgErr != nil {
		t.Fatalf("embedded postgres: %v", pgErr)
	}
	ctx := context.Background()
	name := "t" + strconv.FormatInt(dbSeq.Add(1), 10) + "_" + strconv.Itoa(os.Getpid())
	c, err := adminConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, "CREATE DATABASE "+name+opts); err != nil {
		t.Fatal(err)
	}
	// Production's text search configuration, so search tests stem the same
	// way (an initdb in a C locale would default to pg_catalog.simple).
	if _, err := c.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s SET default_text_search_config = 'pg_catalog.english'", name)); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(ctx, config.Config{
		DBHost: fmt.Sprintf("127.0.0.1:%d", pgPort),
		DBUser: "postgres", DBPass: "postgres", DBName: name,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.Close()
		ac, err := adminConn(context.Background())
		if err != nil {
			return
		}
		defer ac.Close(context.Background())
		ac.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return d
}

// AddUser inserts a user named name, logged in just now, and returns its
// id. (The column has no default: signup leaves it NULL until a login.)
func AddUser(t testing.TB, d *db.DB, name string) string {
	t.Helper()
	var id string
	err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO users (id, username, email, verified, last_login) VALUES (substr(md5(random()::text), 1, 22), $1, $2, true, now()) RETURNING id`,
		name, name+"@example.com").Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// AddConnection inserts a connection owned by user (added if missing), on the main liercd
// (liercd 'main'; the column's default is 'next').
func AddConnection(t testing.TB, d *db.DB, id, user, config string) {
	t.Helper()
	ensureUser(t, d, user)
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO connections (id, user_id, config, liercd) VALUES ($1, $2, $3, 'main')`, id, user, config); err != nil {
		t.Fatal(err)
	}
}

// EnsureConnection adds connection conn, owned by user, and the user,
// unless they exist: rows inserted by hand need their owners
// (logs_connection_id_fkey and the other keys of migration 3).
func EnsureConnection(t testing.TB, d *db.DB, conn, user string) {
	t.Helper()
	ensureUser(t, d, user)
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO connections (id, user_id, config) VALUES ($1, $2, '{}') ON CONFLICT (id) DO NOTHING`, conn, user); err != nil {
		t.Fatal(err)
	}
}

// ensureUser adds a user with id (named after it) unless one exists, so
// fixtures can name an owner without creating it: every connection needs
// its user (connections_user_id_fkey).
func ensureUser(t testing.TB, d *db.DB, id string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO users (id, username, email, verified, last_login)
		 VALUES ($1::text, 'u_' || left(md5($1::text), 24), md5($1::text) || '@example.com', true, now()) ON CONFLICT (id) DO NOTHING`, id); err != nil {
		t.Fatal(err)
	}
}

// AddLog inserts a log row with the given id for user's connection conn
// (both added if missing).
func AddLog(t testing.TB, d *db.DB, id int64, conn, user string) {
	t.Helper()
	EnsureConnection(t, d, conn, user)
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO logs (id, connection_id, user_id, channel, time, message) VALUES ($1, $2, $3, '#a', now(), '{}')`,
		id, conn, user); err != nil {
		t.Fatal(err)
	}
}
