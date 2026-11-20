// Package testenv provides Postgres, nsqd and liercd fakes for tests.
package testenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/passwords"
)

// The tests' Postgres is a server at PostgresEnv's URL when that's set (CI
// sets it to a service container). Otherwise it's one Docker container,
// pgContainer, started by the first test that needs it and left running
// for the next run, shared by every test process: each test gets its own
// database, cloned from a template the migrations build.
const (
	PostgresEnv = "LIERC_TEST_POSTGRES"
	// pgImage is production's version; CI's service container uses the same
	pgImage     = "postgres:18.7"
	pgContainer = "lierc-api-test-postgres"
	pgPassword  = "postgres"
)

// testDBName matches the databases newDatabase creates: t_<unix
// seconds>_<pid>_<n>. Those from runs that ended without dropping them
// (killed) are dropped when they're an hour old.
var testDBName = regexp.MustCompile(`^t_([0-9]+)_[0-9]+_[0-9]+$`)

const staleTestDB = time.Hour

var (
	pgOnce     sync.Once
	pgErr      error
	pgURL      string // the admin connection's URL
	templateDB string
	dbSeq      atomic.Int64
)

// Main is for TestMain: it runs the tests, then removes the nsqd binary
// NSQD builds. The Postgres container is left running for the next run.
func Main(m *testing.M) {
	passwords.UseTestCost()
	code := m.Run()
	if nsqdDir != "" {
		os.RemoveAll(nsqdDir)
	}
	os.Exit(code)
}

func startPostgres() error {
	ctx := context.Background()
	if u := os.Getenv(PostgresEnv); u != "" {
		pgURL = u
	} else {
		u, err := startContainer(ctx)
		if err != nil {
			return fmt.Errorf("%w (or set %s to a Postgres URL)", err, PostgresEnv)
		}
		pgURL = u
	}
	if err := waitForPostgres(ctx, 60*time.Second); err != nil {
		return err
	}
	if err := dropStaleDatabases(ctx); err != nil {
		return err
	}
	return createTemplate(ctx)
}

// pgRun is how pgContainer is made: its data on a tmpfs, without fsync,
// since nothing in it outlives the container, and trust authentication,
// since it listens only on 127.0.0.1 and holds only test data (SCRAM's
// key derivation, done in Go on every new connection, took more than
// half the database tests' time under -race).
var pgRun = []string{
	"-e", "POSTGRES_PASSWORD=" + pgPassword, "-e", "POSTGRES_HOST_AUTH_METHOD=trust",
	"-p", "127.0.0.1::5432", "--tmpfs", "/var/lib/postgresql", pgImage,
	"-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off",
	"-c", "max_connections=500",
}

// pgRunLabel is the container label holding a hash of pgRun, so a
// container made with other settings (an older pgRun, another image) is
// replaced.
const pgRunLabel = "lierc-api-test.run"

func pgRunHash() string {
	sum := sha256.Sum256([]byte(strings.Join(pgRun, "\x00")))
	return hex.EncodeToString(sum[:])[:12]
}

// startContainer starts pgContainer, or finds it running (another test
// process, or an earlier run, started it), and returns its admin URL.
func startContainer(ctx context.Context) (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", errors.New("the tests' Postgres needs docker")
	}
	want := pgRunHash()
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		`{{index .Config.Labels "`+pgRunLabel+`"}}`, pgContainer).Output()
	if err == nil && strings.TrimSpace(string(out)) != want {
		// made with other settings; another test process may be
		// replacing it too
		exec.CommandContext(ctx, "docker", "rm", "-f", pgContainer).Run()
	}
	if url, ok := containerURL(ctx); ok {
		return url, nil
	}
	// stopped, or never made: start it, or make it (losing a race with
	// another test process to make it is fine)
	if exec.CommandContext(ctx, "docker", "start", pgContainer).Run() != nil {
		args := append([]string{"run", "-d", "--name", pgContainer, "--label", pgRunLabel + "=" + want}, pgRun...)
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil && !strings.Contains(string(out), "is already in use") {
			return "", fmt.Errorf("starting the tests' Postgres (docker run %s): %v: %s", pgImage, err, out)
		}
	}
	for range 50 {
		if url, ok := containerURL(ctx); ok {
			return url, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("the tests' Postgres container %s didn't start", pgContainer)
}

// containerURL is the running container's admin URL, from the host port
// Docker gave it.
func containerURL(ctx context.Context) (string, bool) {
	out, err := exec.CommandContext(ctx, "docker", "port", pgContainer, "5432/tcp").Output()
	if err != nil {
		return "", false
	}
	// "127.0.0.1:49153", one line per address
	addr := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if addr == "" {
		return "", false
	}
	return fmt.Sprintf("postgres://postgres:%s@%s/postgres?sslmode=disable", pgPassword, addr), true
}

// waitForPostgres waits until the server takes connections, as a just
// started container doesn't at once.
func waitForPostgres(ctx context.Context, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		c, err := adminConn(ctx)
		if err == nil {
			c.Close(ctx)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the tests' Postgres isn't taking connections: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func adminConn(ctx context.Context) (*pgx.Conn, error) {
	return pgx.Connect(ctx, pgURL)
}

// dropStaleDatabases drops the test databases of runs that ended without
// dropping theirs, once they're staleTestDB old, so a parallel run's
// aren't touched.
func dropStaleDatabases(ctx context.Context) error {
	c, err := adminConn(ctx)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	rows, err := c.Query(ctx, "SELECT datname FROM pg_database")
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-staleTestDB).Unix()
	for _, name := range names {
		m := testDBName.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		if at, _ := strconv.ParseInt(m[1], 10, 64); at < cutoff {
			// another process may be dropping it too
			c.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		}
	}
	return nil
}

// createTemplate makes sure the template database for these migrations
// exists, building it if not: named after a hash of the migration files,
// so a shared server holds one per set of migrations (worktrees on
// different branches don't clash), and built under an advisory lock, so
// concurrent test processes build it once. It's migrated under a
// temporary name and renamed when done, so a killed build is never used.
func createTemplate(ctx context.Context) error {
	sum, err := migrationsHash()
	if err != nil {
		return err
	}
	templateDB = "lierc_template_" + sum
	c, err := adminConn(ctx)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, "SELECT pg_advisory_lock(hashtext($1))", templateDB); err != nil {
		return err
	}
	defer c.Exec(ctx, "SELECT pg_advisory_unlock(hashtext($1))", templateDB)

	var exists bool
	if err := c.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", templateDB).Scan(&exists); err != nil || exists {
		return err
	}
	building := templateDB + "_building"
	if _, err := c.Exec(ctx, "DROP DATABASE IF EXISTS "+building+" WITH (FORCE)"); err != nil {
		return err
	}
	if _, err := c.Exec(ctx, "CREATE DATABASE "+building); err != nil {
		return err
	}
	d, err := db.Open(ctx, configFor(building))
	if err != nil {
		return err
	}
	err = d.Migrate(ctx, db.MigrateOptions{})
	// Closed before the rename and any clone: both need the database to
	// have no connections.
	d.Close()
	if err != nil {
		return err
	}
	_, err = c.Exec(ctx, "ALTER DATABASE "+building+" RENAME TO "+templateDB)
	return err
}

// migrationsHash is the first 12 hex digits of a hash of the embedded
// migrations' names and contents.
func migrationsHash() (string, error) {
	files := db.Migrations()
	var names []string
	if err := fs.WalkDir(files, ".", func(path string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			names = append(names, path)
		}
		return err
	}); err != nil {
		return "", err
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		b, err := fs.ReadFile(files, name)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// configFor is the config for database name on the tests' server.
func configFor(name string) config.Config {
	cc, err := pgx.ParseConfig(pgURL)
	if err != nil {
		panic(err) // pgURL was connected to already
	}
	return config.Config{
		DBHost: fmt.Sprintf("%s:%d", cc.Host, cc.Port),
		DBUser: cc.User, DBPass: cc.Password, DBName: name,
	}
}

// Postgres returns a DB backed by a fresh database cloned from the
// migrated template.
func Postgres(t testing.TB) *db.DB {
	t.Helper()
	return newDatabase(t, true)
}

// EmptyPostgres returns a DB backed by a fresh database with no tables.
func EmptyPostgres(t testing.TB) *db.DB {
	t.Helper()
	return newDatabase(t, false)
}

// newDatabase creates a database, a clone of the migrated template when
// migrated is set (the template is known only once the server is up).
func newDatabase(t testing.TB, migrated bool) *db.DB {
	t.Helper()
	pgOnce.Do(func() { pgErr = startPostgres() })
	if pgErr != nil {
		t.Fatalf("tests' postgres: %v", pgErr)
	}
	ctx := context.Background()
	name := fmt.Sprintf("t_%d_%d_%d", time.Now().Unix(), os.Getpid(), dbSeq.Add(1))
	c, err := adminConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	create := "CREATE DATABASE " + name
	if migrated {
		create += " TEMPLATE " + templateDB
	}
	if _, err := c.Exec(ctx, create); err != nil {
		t.Fatal(err)
	}
	// Production's text search configuration, so search tests stem the same
	// way (an initdb in a C locale would default to pg_catalog.simple).
	if _, err := c.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s SET default_text_search_config = 'pg_catalog.english'", name)); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(ctx, configFor(name))
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
// unless they exist, on the main liercd like AddConnection: log, archive
// and seen rows inserted by hand need their connection, and their user_id
// must be its owner (the (connection_id, user_id) keys of migration 4).
func EnsureConnection(t testing.TB, d *db.DB, conn, user string) {
	t.Helper()
	ensureUser(t, d, user)
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO connections (id, user_id, config, liercd) VALUES ($1, $2, '{}', 'main') ON CONFLICT (id) DO NOTHING`, conn, user); err != nil {
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
