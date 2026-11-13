package lctl

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/admin"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

type out struct{ stdout, stderr bytes.Buffer }

func (o *out) sys(stdin string, terminal bool) System {
	return System{
		Getenv:          func(string) string { return "" },
		Stdin:           strings.NewReader(stdin),
		Stdout:          &o.stdout,
		Stderr:          &o.stderr,
		StdinIsTerminal: terminal,
	}
}

var testCommands = []Command{
	{Name: "alpha", Summary: "Do the first thing", Run: func(ctx context.Context, env *Env, args []string) error {
		env.Stdout.Write([]byte("alpha ran " + strings.Join(args, ",") + "\n"))
		return nil
	}},
	{Name: "bad-args", Summary: "Always a usage error", Run: func(ctx context.Context, env *Env, args []string) error {
		return &UsageError{Msg: "needs an argument"}
	}},
	{Name: "fails", Summary: "Always fails", Run: func(ctx context.Context, env *Env, args []string) error {
		return errors.New("it broke")
	}},
	{Name: "flags", Summary: "Has flags", Run: func(ctx context.Context, env *Env, args []string) error {
		fs := NewFlags(env, "flags", "flags [--n N]\n\nTakes a number.")
		n := fs.Int("n", 1, "a number")
		if err := Parse(fs, args); err != nil {
			return err
		}
		env.Stdout.Write([]byte(strings.Repeat("x", *n) + "\n"))
		return nil
	}},
}

// noOpen fails the test if a command that shouldn't need the database
// asks for an Env.
func noOpen(t *testing.T) Opener {
	return func(ctx context.Context, sys System, needsDB bool) (*Env, func(), error) {
		t.Fatal("opened an Env")
		return nil, nil, nil
	}
}

func fakeOpen(sys System) Opener {
	return func(ctx context.Context, s System, needsDB bool) (*Env, func(), error) {
		return &Env{Stdin: s.Stdin, Stdout: s.Stdout, Stderr: s.Stderr, StdinIsTerminal: s.StdinIsTerminal}, func() {}, nil
	}
}

func TestHelpListsCommandsWithoutDB(t *testing.T) {
	for _, args := range [][]string{{"help"}, {}} {
		var o out
		if code := mainGroups(context.Background(), args, o.sys("", true), testCommands, nil, noOpen(t)); code != 0 {
			t.Fatalf("%v: exit %d", args, code)
		}
		for _, c := range testCommands {
			if !strings.Contains(o.stdout.String(), c.Name) || !strings.Contains(o.stdout.String(), c.Summary) {
				t.Errorf("%v: help is missing %s: %q", args, c.Name, o.stdout.String())
			}
		}
	}
}

func TestRealCommandsAreListed(t *testing.T) {
	var o out
	mainGroups(context.Background(), []string{"help"}, o.sys("", true), Commands, nil, noOpen(t))
	for _, c := range Commands {
		if !strings.Contains(o.stdout.String(), c.Name) {
			t.Errorf("help is missing %s", c.Name)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	var o out
	if code := mainGroups(context.Background(), []string{"nope"}, o.sys("", true), testCommands, nil, noOpen(t)); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(o.stderr.String(), "usage:") || !strings.Contains(o.stderr.String(), "unknown command nope") {
		t.Fatalf("stderr %q", o.stderr.String())
	}
}

func TestRunsCommandWithArgs(t *testing.T) {
	var o out
	sys := o.sys("", true)
	if code := mainGroups(context.Background(), []string{"alpha", "a", "b"}, sys, testCommands, nil, fakeOpen(sys)); code != 0 {
		t.Fatalf("exit %d: %s", code, o.stderr.String())
	}
	if o.stdout.String() != "alpha ran a,b\n" {
		t.Fatalf("stdout %q", o.stdout.String())
	}
}

func TestUsageErrorExits2(t *testing.T) {
	var o out
	sys := o.sys("", true)
	if code := mainGroups(context.Background(), []string{"bad-args"}, sys, testCommands, nil, fakeOpen(sys)); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(o.stderr.String(), "lctl bad-args: needs an argument") {
		t.Fatalf("stderr %q", o.stderr.String())
	}
}

func TestErrorExits1(t *testing.T) {
	var o out
	sys := o.sys("", true)
	if code := mainGroups(context.Background(), []string{"fails"}, sys, testCommands, nil, fakeOpen(sys)); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(o.stderr.String(), "lctl fails: it broke") {
		t.Fatalf("stderr %q", o.stderr.String())
	}
}

func TestOpenErrorExits1(t *testing.T) {
	var o out
	failing := func(context.Context, System, bool) (*Env, func(), error) { return nil, nil, errors.New("no database") }
	if code := mainGroups(context.Background(), []string{"alpha"}, o.sys("", true), testCommands, nil, failing); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(o.stderr.String(), "no database") {
		t.Fatalf("stderr %q", o.stderr.String())
	}
}

func TestFlags(t *testing.T) {
	var o out
	sys := o.sys("", true)
	if code := mainGroups(context.Background(), []string{"flags", "--n", "3"}, sys, testCommands, nil, fakeOpen(sys)); code != 0 || o.stdout.String() != "xxx\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, o.stdout.String(), o.stderr.String())
	}
	o = out{}
	sys = o.sys("", true)
	if code := mainGroups(context.Background(), []string{"flags", "-h"}, sys, testCommands, nil, fakeOpen(sys)); code != 0 {
		t.Fatalf("-h: exit %d", code)
	}
	if !strings.Contains(o.stderr.String(), "usage: lctl flags [--n N]") || !strings.Contains(o.stderr.String(), "Takes a number.") {
		t.Fatalf("-h: stderr %q", o.stderr.String())
	}
	o = out{}
	sys = o.sys("", true)
	if code := mainGroups(context.Background(), []string{"flags", "--bogus"}, sys, testCommands, nil, fakeOpen(sys)); code != 2 {
		t.Fatalf("bad flag: exit %d, want 2", code)
	}
}

func TestReadIDs(t *testing.T) {
	env := &Env{Stdin: strings.NewReader(" a \n\nb\n"), StdinIsTerminal: false}
	if got, _ := ReadIDs(env, []string{"x", "y"}); strings.Join(got, ",") != "x,y" {
		t.Errorf("args: %v", got)
	}
	if got, _ := ReadIDs(env, nil); strings.Join(got, ",") != "a,b" {
		t.Errorf("stdin: %v", got)
	}
	env = &Env{Stdin: strings.NewReader("a\n"), StdinIsTerminal: true}
	if got, _ := ReadIDs(env, nil); len(got) != 0 {
		t.Errorf("terminal: %v", got)
	}
}

func TestEnvFromSystem(t *testing.T) {
	vars := map[string]string{"POSTGRES_HOST": "127.0.0.1:1", "POSTGRES_DB": "other", "LIERCD_HOST": "liercd", "API_STATS": "http://s/stats"}
	var o out
	sys := o.sys("", false)
	sys.Getenv = func(k string) string { return vars[k] }
	env, closeEnv, err := OpenEnv(context.Background(), sys, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeEnv()
	if env.Config.DBName != "other" {
		t.Errorf("DBName %q, want POSTGRES_DB", env.Config.DBName)
	}
	if env.Liercd.BaseURL != "http://liercd:5005" || env.StatsURL != "http://s/stats" {
		t.Errorf("liercd %q stats %q", env.Liercd.BaseURL, env.StatsURL)
	}
	delete(vars, "POSTGRES_DB")
	delete(vars, "API_STATS")
	env2, close2, err := OpenEnv(context.Background(), sys, true)
	if err != nil {
		t.Fatal(err)
	}
	defer close2()
	if env2.Config.DBName != "lierc" || env2.StatsURL != "http://127.0.0.1:5009/stats" {
		t.Errorf("defaults: db %q stats %q", env2.Config.DBName, env2.StatsURL)
	}
	if env2.Resolve == nil || env2.Sleep == nil || env2.ReadPassword == nil {
		t.Error("Resolve, Sleep and ReadPassword must be set")
	}
	if env2.Admin.BaseURL != "http://127.0.0.1:5009" {
		t.Errorf("default admin %q", env2.Admin.BaseURL)
	}

	// API_STATS defaults to API_ADMIN's /stats; API_STATS still wins
	vars["API_ADMIN"] = "http://adm:9/"
	env3, close3, err := OpenEnv(context.Background(), sys, false)
	if err != nil {
		t.Fatal(err)
	}
	defer close3()
	if env3.Admin.BaseURL != "http://adm:9" || env3.StatsURL != "http://adm:9/stats" {
		t.Errorf("API_ADMIN: admin %q stats %q", env3.Admin.BaseURL, env3.StatsURL)
	}
	vars["API_STATS"] = "http://s/stats"
	env4, close4, err := OpenEnv(context.Background(), sys, false)
	if err != nil {
		t.Fatal(err)
	}
	defer close4()
	if env4.StatsURL != "http://s/stats" {
		t.Errorf("API_STATS with API_ADMIN: stats %q", env4.StatsURL)
	}
}

func TestSleepCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, 1e9); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestPoolBegin(t *testing.T) {
	d := testenv.Postgres(t)
	tx, err := d.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `INSERT INTO pref ("user", name, value) VALUES ('u', 'n', 'v')`); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(context.Background())
	var n int
	d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pref`).Scan(&n)
	if n != 0 {
		t.Fatalf("rolled back insert is visible: %d rows", n)
	}
}

// The wrapper sets LCTL_STDIN_TTY=1 when the operator's stdin is a terminal
// but the container's isn't (stdout piped or redirected), so lctl doesn't
// wait for ids on it.
func TestOSSystemHonoursWrapperTTY(t *testing.T) {
	t.Setenv("LCTL_STDIN_TTY", "1")
	if !OSSystem().StdinIsTerminal {
		t.Fatal("LCTL_STDIN_TTY=1 not treated as a terminal")
	}
}

func TestReadIDsEmptyStdin(t *testing.T) {
	env := &Env{Stdin: strings.NewReader("\n \n"), StdinIsTerminal: false}
	if ids, err := ReadIDs(env, nil); !errors.Is(err, ErrNoIDsOnStdin) || len(ids) != 0 {
		t.Fatalf("got %v %v", ids, err)
	}
}

func TestRemovedCommands(t *testing.T) {
	names := []string{"fix-config-encoding", "convert-legacy-passwords", "move-connection", "move-connections", "check-connections", "reset"}
	for _, name := range names {
		var o out
		if code := Main(context.Background(), []string{name}, o.sys("", true)); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
		if !strings.Contains(o.stderr.String(), "unknown command "+name) {
			t.Errorf("%s: stderr %q lacks unknown command", name, o.stderr.String())
		}
	}
	var o out
	Main(context.Background(), []string{"help"}, o.sys("", true))
	for _, name := range names {
		for _, line := range strings.Split(o.stdout.String(), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), name+" ") {
				t.Errorf("help still lists %s", name)
			}
		}
	}
}

var portedNames = []string{"users", "user-info", "inactive-users", "delete-users", "connection-list", "connection-info", "reconnect", "stop-inactive", "delete-connections", "logins", "end-login", "prune-logins", "prune-seen", "stats"}

// helpLines is the command names a help text lists.
func helpLines(s string) map[string]bool {
	names := map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "  ") {
			if f := strings.Fields(line); len(f) > 0 {
				names[f[0]] = true
			}
		}
	}
	return names
}

func TestAdminGroup(t *testing.T) {
	ctx := context.Background()
	// a ported command's -h works with no database
	var o out
	if code := Main(ctx, []string{"admin", "user-info", "-h"}, o.sys("", true)); code != 0 {
		t.Fatalf("admin user-info -h: exit %d: %s", code, o.stderr.String())
	}
	if !strings.Contains(o.stderr.String(), "usage: lctl admin user-info USER...") {
		t.Errorf("-h stderr %q", o.stderr.String())
	}

	for _, args := range [][]string{{"admin"}, {"admin", "help"}} {
		o = out{}
		if code := Main(ctx, args, o.sys("", true)); code != 0 {
			t.Fatalf("%v: exit %d", args, code)
		}
		got := helpLines(o.stdout.String())
		if len(got) != len(portedNames) {
			t.Errorf("%v lists %v, want the %d ported commands", args, got, len(portedNames))
		}
		for _, n := range portedNames {
			if !got[n] {
				t.Errorf("%v is missing %s", args, n)
			}
		}
	}

	o = out{}
	if code := Main(ctx, []string{"help"}, o.sys("", true)); code != 0 {
		t.Fatalf("help: exit %d", code)
	}
	got := helpLines(o.stdout.String())
	for _, n := range []string{"archive", "revive", "delete-orphan-logs", "admin"} {
		if !got[n] {
			t.Errorf("help is missing %s", n)
		}
	}
	for _, n := range portedNames {
		if got[n] {
			t.Errorf("help still lists %s", n)
		}
	}

	o = out{}
	if code := Main(ctx, []string{"user-info", "x"}, o.sys("", true)); code != 2 {
		t.Errorf("old name: exit %d, want 2", code)
	}
	if o.stderr.String() != "lctl: user-info is now \"lctl admin user-info\"\n" {
		t.Errorf("old name: stderr %q", o.stderr.String())
	}

	o = out{}
	if code := Main(ctx, []string{"admin", "archive"}, o.sys("", true)); code != 2 {
		t.Errorf("admin archive: exit %d, want 2", code)
	}
	if !strings.Contains(o.stderr.String(), "unknown admin command archive") {
		t.Errorf("admin archive: stderr %q", o.stderr.String())
	}
}

// The ported commands talk to the admin API (and liercd), never to
// Postgres; the commands that stay on the database still open it.
func TestPortedCommandsNeedNoDB(t *testing.T) {
	h := newHarness(t)
	u := h.user("alice")
	testenv.AddConnection(t, h.db, "c1", u, `{"Host":"irc.example.com","Port":6667}`)
	srv := httptest.NewServer(admin.New(h.db, nil))
	defer srv.Close()

	vars := map[string]string{
		"POSTGRES_HOST": "127.0.0.1:1", // nothing listens: a query would fail
		"API_ADMIN":     srv.URL,
		"LIERCD_HOST":   strings.TrimPrefix(h.liercd.URL, "http://"),
	}
	var o out
	sys := o.sys("", true)
	sys.Getenv = func(k string) string { return vars[k] }
	if code := Main(context.Background(), []string{"admin", "connection-list"}, sys); code != 0 {
		t.Fatalf("connection-list: exit %d: %s", code, o.stderr.String())
	}
	if !strings.Contains(o.stdout.String(), "c1") || !strings.Contains(o.stdout.String(), "alice") {
		t.Errorf("connection-list stdout %q", o.stdout.String())
	}

	env, release, err := OpenEnv(context.Background(), sys, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if env.Store != nil {
		t.Error("OpenEnv without needsDB opened the store")
	}
	env, release, err = OpenEnv(context.Background(), sys, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if env.Store == nil {
		t.Error("OpenEnv with needsDB has no store")
	}

	// archive uses the store: with Postgres unreachable it fails
	o = out{}
	sys = o.sys("", true)
	sys.Getenv = func(k string) string { return vars[k] }
	if code := Main(context.Background(), []string{"archive"}, sys); code != 1 {
		t.Errorf("archive: exit %d, want 1 (stderr %q)", code, o.stderr.String())
	}
	for _, c := range append(append([]Command{}, Commands...), AdminCommands...) {
		want := c.Name == "archive" || c.Name == "revive" || c.Name == "delete-orphan-logs"
		if c.NeedsDB != want {
			t.Errorf("%s: NeedsDB = %v, want %v", c.Name, c.NeedsDB, want)
		}
	}
}

// revive runs from the liercd units' startup, so it mustn't depend on the
// API: its liercd calls route through the store, not the admin server.
func TestReviveRoutesWithoutAdmin(t *testing.T) {
	h := newHarness(t)
	u := h.user("recent")
	h.exec(`UPDATE "user" SET last_login = now() - interval '1 hour' WHERE id = $1`, u)
	testenv.AddConnection(t, h.db, "stopped", u, `{"Host":"irc.example.com","Port":6667}`)

	closed := httptest.NewServer(nil)
	closed.Close() // nothing answers at its address
	cc := h.db.Pool.Config().ConnConfig
	vars := map[string]string{
		"POSTGRES_HOST":     net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port))),
		"POSTGRES_USER":     cc.User,
		"POSTGRES_PASSWORD": cc.Password,
		"POSTGRES_DB":       cc.Database,
		"API_ADMIN":         closed.URL,
		"LIERCD_HOST":       strings.TrimPrefix(h.liercd.URL, "http://"),
	}
	var o out
	sys := o.sys("", true)
	sys.Getenv = func(k string) string { return vars[k] }
	if code := Main(context.Background(), []string{"revive"}, sys); code != 0 {
		t.Fatalf("revive: exit %d: %s", code, o.stderr.String())
	}
	if !strings.Contains(o.stdout.String(), "started stopped") {
		t.Errorf("stdout %q", o.stdout.String())
	}
	var got []string
	for _, c := range h.liercd.Calls() {
		got = append(got, c.Method+" "+c.Path)
	}
	if want := "GET /stopped/status,POST /stopped/create"; strings.Join(got, ",") != want {
		t.Errorf("liercd calls %v, want %s", got, want)
	}
}

// archive opens the database itself and works with the admin server down.
func TestArchiveOpensStoreWithoutAdmin(t *testing.T) {
	h := newHarness(t)
	closed := httptest.NewServer(nil)
	closed.Close()
	cc := h.db.Pool.Config().ConnConfig
	vars := map[string]string{
		"POSTGRES_HOST":     net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port))),
		"POSTGRES_USER":     cc.User,
		"POSTGRES_PASSWORD": cc.Password,
		"POSTGRES_DB":       cc.Database,
		"API_ADMIN":         closed.URL,
	}
	var o out
	sys := o.sys("", true)
	sys.Getenv = func(k string) string { return vars[k] }
	if code := Main(context.Background(), []string{"archive"}, sys); code != 0 {
		t.Fatalf("archive: exit %d: %s", code, o.stderr.String())
	}
	if strings.Contains(o.stderr.String(), "reachable") {
		t.Errorf("archive touched the admin server: %s", o.stderr.String())
	}
}

// An admin command's usage names it as "lctl admin NAME" however it's run.
func TestAdminUsagePrefix(t *testing.T) {
	h := newHarness(t)
	if err := h.run("user-info", "-h"); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("got %v", err)
	}
	if !strings.HasPrefix(h.stderr.String(), "usage: lctl admin user-info") {
		t.Errorf("usage = %q", h.stderr.String())
	}
	if err := h.run("archive", "-h"); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("archive -h: %v", err)
	}
	if !strings.HasPrefix(h.stderr.String(), "usage: lctl archive") {
		t.Errorf("archive usage = %q", h.stderr.String())
	}
}
