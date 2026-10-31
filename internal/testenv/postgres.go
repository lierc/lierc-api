// Package testenv provides in-process Postgres, nsqd and liercd fakes for tests.
package testenv

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
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
)

const templateDB = "lierc_template"

var (
	pgOnce   sync.Once
	pgErr    error
	pgServer *embeddedpg.EmbeddedPostgres
	pgPort   uint32
	pgDir    string
	dbSeq    atomic.Int64
)

// Main is for TestMain: it runs the tests, then stops the embedded Postgres
// (started lazily by the first Postgres call) and removes its files.
func Main(m *testing.M) {
	code := m.Run()
	if pgServer != nil {
		pgServer.Stop()
	}
	if pgDir != "" {
		os.RemoveAll(pgDir)
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
	cfg := embeddedpg.DefaultConfig().
		Port(pgPort).
		RuntimePath(filepath.Join(pgDir, "run")).
		DataPath(filepath.Join(pgDir, "data")).
		StartTimeout(120 * time.Second).
		Logger(nil)
	pgServer = embeddedpg.NewDatabase(cfg)
	if err := pgServer.Start(); err != nil {
		pgServer = nil
		return err
	}
	return createTemplate()
}

func adminConn(ctx context.Context) (*pgx.Conn, error) {
	return pgx.Connect(ctx, fmt.Sprintf(
		"postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", pgPort))
}

func schema() (string, error) {
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "lierc.sql"))
	if err != nil {
		return "", err
	}
	return PatchSchema(string(b))
}

// PatchSchema fixes the two statements in lierc.sql that Postgres rejects:
// log_user_highlight lacks "on log", and log_tag_msgid lacks a trailing ';'.
// The file itself stays verbatim; callers patch it in memory (tests) or when
// loading it into a database.
func PatchSchema(s string) (string, error) {
	for _, r := range [][2]string{
		{"create index log_user_highlight (", "create index log_user_highlight on log ("},
		{"->'msgid'))\ncreate index on log (time);", "->'msgid'));\ncreate index on log (time);"},
	} {
		if !strings.Contains(s, r[0]) {
			return "", fmt.Errorf("lierc.sql no longer contains %q; update testenv patches", r[0])
		}
		s = strings.Replace(s, r[0], r[1], 1)
	}
	return s, nil
}

func createTemplate() error {
	ctx := context.Background()
	sql, err := schema()
	if err != nil {
		return err
	}
	c, err := adminConn(ctx)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, "CREATE DATABASE "+templateDB); err != nil {
		return err
	}
	tc, err := pgx.Connect(ctx, fmt.Sprintf(
		"postgres://postgres:postgres@127.0.0.1:%d/%s?sslmode=disable", pgPort, templateDB))
	if err != nil {
		return err
	}
	defer tc.Close(ctx)
	_, err = tc.Exec(ctx, sql) // no args: simple protocol, multiple statements
	return err
}

// Postgres returns a DB backed by a fresh database cloned from lierc.sql.
func Postgres(t testing.TB) *db.DB {
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
	if _, err := c.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateDB)); err != nil {
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

// AddUser inserts a user named name and returns its id.
func AddUser(t testing.TB, d *db.DB, name string) string {
	t.Helper()
	var id string
	err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO "user" (id, username, email, password) VALUES (substr(md5(random()::text), 1, 22), $1, $2, 'x') RETURNING id`,
		name, name+"@example.com").Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// AddConnection inserts a connection owned by user.
func AddConnection(t testing.TB, d *db.DB, id, user, config string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO connection (id, "user", config) VALUES ($1, $2, $3)`, id, user, config); err != nil {
		t.Fatal(err)
	}
}

// AddLog inserts a log row with the given id for user's connection conn.
func AddLog(t testing.TB, d *db.DB, id int64, conn, user string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO log (id, connection, "user", channel, time, message) VALUES ($1, $2, $3, '#a', now(), '{}')`,
		id, conn, user); err != nil {
		t.Fatal(err)
	}
}
