package lctl

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/admin"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/testenv"
)

type harness struct {
	t      *testing.T
	db     *db.DB
	liercd *testenv.LiercdServer
	env    *Env
	stdout bytes.Buffer
	stderr bytes.Buffer
	vars   map[string]string
	hosts  map[string][]string
	slept  []time.Duration
	// beforeSleep, if set, runs when a command waits.
	beforeSleep func()
	sleepErr    error
	// announced is what the admin server told the stream server.
	announced []db.ConnectionChange
	annMu     sync.Mutex
}

func (h *harness) announce(_ context.Context, c db.ConnectionChange) {
	h.annMu.Lock()
	defer h.annMu.Unlock()
	h.announced = append(h.announced, c)
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, db: testenv.Postgres(t), liercd: testenv.FakeLiercd(t), vars: map[string]string{}, hosts: map[string][]string{}}
	h.env = &Env{
		Getenv: func(k string) string { return h.vars[k] },
		Store:  h.db,
		Liercd: &liercd.Client{BaseURL: h.liercd.URL, HTTP: http.DefaultClient},
		Stdin:  strings.NewReader(""), Stdout: &h.stdout, Stderr: &h.stderr, StdinIsTerminal: true,
		Resolve: func(ctx context.Context, host string) ([]string, error) { return h.hosts[host], nil },
		Sleep: func(ctx context.Context, d time.Duration) error {
			h.slept = append(h.slept, d)
			if h.beforeSleep != nil {
				h.beforeSleep()
			}
			return h.sleepErr
		},
	}
	srv := httptest.NewServer(admin.New(h.db, h.announce))
	t.Cleanup(srv.Close)
	h.env.Admin = &Admin{BaseURL: srv.URL, HTTP: http.DefaultClient}
	return h
}

// run runs the named command and returns its error.
func (h *harness) run(name string, args ...string) error {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	for _, list := range [][]Command{Commands, AdminCommands} {
		for _, c := range list {
			if c.Name == name {
				return c.Run(context.Background(), h.env, args)
			}
		}
	}
	h.t.Fatalf("no command %s", name)
	return nil
}

func (h *harness) exec(sql string, args ...any) {
	h.t.Helper()
	if _, err := h.db.Pool.Exec(context.Background(), sql, args...); err != nil {
		h.t.Fatalf("%s: %v", sql, err)
	}
}

func (h *harness) count(sql string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		h.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// user adds a user and returns its id.
func (h *harness) user(name string) string { return testenv.AddUser(h.t, h.db, name) }

// conn adds a connection with a log, seen and private row.
func (h *harness) conn(id, user, config string) {
	testenv.AddConnection(h.t, h.db, id, user, config)
	h.exec(`INSERT INTO log (connection, "user", channel, time, message) VALUES ($1, $2, 'status', now(), '{}')`, id, user)
	h.exec(`INSERT INTO seen ("user", connection, channel, message_id) VALUES ($1, $2, 'status', 1)`, user, id)
	h.exec(`INSERT INTO private (connection, nick, time) VALUES ($1, 'someone', now())`, id)
}

func (h *harness) out() string { return h.stdout.String() }

func (h *harness) has(want ...string) {
	h.t.Helper()
	for _, w := range want {
		if !strings.Contains(h.out(), w) {
			h.t.Errorf("output is missing %q:\n%s", w, h.out())
		}
	}
}

func (h *harness) lacks(not ...string) {
	h.t.Helper()
	for _, w := range not {
		if strings.Contains(h.out(), w) {
			h.t.Errorf("output has %q:\n%s", w, h.out())
		}
	}
}

func (h *harness) calls(method, suffix string) []string {
	var ids []string
	for _, c := range h.liercd.Calls() {
		if c.Method == method && strings.HasSuffix(c.Path, suffix) {
			ids = append(ids, strings.TrimSuffix(strings.TrimPrefix(c.Path, "/"), suffix))
		}
	}
	return ids
}

func asUsage(err error, target **UsageError) bool { return errors.As(err, target) }

// moveHarness routes env.Liercd to two fake liercds that log into one
// ordered list.
type moveHarness struct {
	*harness
	main, next       *testenv.LiercdServer
	mainURL, nextURL string
	mu               sync.Mutex
	order            []string
	// after, if set, runs after each fake answers a request.
	after func(name, method, path string)
}

func newMoveHarness(t *testing.T) *moveHarness {
	m := &moveHarness{harness: newHarness(t), main: testenv.NewLiercd(), next: testenv.NewLiercd()}
	serve := func(name string, f *testenv.LiercdServer) string {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.mu.Lock()
			m.order = append(m.order, name+" "+r.Method+" "+r.URL.Path)
			m.mu.Unlock()
			f.ServeHTTP(w, r)
			if m.after != nil {
				m.after(name, r.Method, r.URL.Path)
			}
		}))
		t.Cleanup(s.Close)
		return s.URL
	}
	m.mainURL, m.nextURL = serve("main", m.main), serve("next", m.next)
	m.env.Liercd = liercd.NewRoutedForTest(m.mainURL, m.nextURL, m.db.LookupLiercd)
	m.env.Config.LiercdNextHost = "next.example"
	return m
}

func (m *moveHarness) column(id string) string {
	m.t.Helper()
	inst, err := m.db.LookupLiercd(context.Background(), id)
	if err != nil {
		m.t.Fatal(err)
	}
	return inst
}

func (m *moveHarness) calls() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(m.order, "|")
}

func (m *moveHarness) calledNothing() {
	m.t.Helper()
	if len(m.order) != 0 {
		m.t.Errorf("liercd was called: %v", m.order)
	}
}

func testenv_conn(h *harness, id, user, config string) {
	h.exec(`INSERT INTO connection (id, "user", config) VALUES ($1, $2, $3)`, id, user, config)
}
