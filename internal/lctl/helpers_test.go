package lctl

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

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
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, db: testenv.Postgres(t), liercd: testenv.FakeLiercd(t), vars: map[string]string{}, hosts: map[string][]string{}}
	h.env = &Env{
		Getenv: func(k string) string { return h.vars[k] },
		DB:     h.db,
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
	return h
}

// run runs the named command and returns its error.
func (h *harness) run(name string, args ...string) error {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	for _, c := range Commands {
		if c.Name == name {
			return c.Run(context.Background(), h.env, args)
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
