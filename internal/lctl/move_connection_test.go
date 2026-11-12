package lctl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/testenv"
)

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

func TestMoveMainToNext(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{"Host":"x"}`)
	m.main.MarkRunning("c1")
	if err := m.run("move-connection", "c1", "next"); err != nil {
		t.Fatal(err)
	}
	want := "next GET /c1/status|main POST /c1/destroy|main POST /c1/destroy|next POST /c1/create"
	if got := m.calls(); got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if got := m.column("c1"); got != "next" {
		t.Errorf("column = %q", got)
	}
	if got := m.next.Calls(); len(got) != 2 || got[1].Path != "/c1/create" || !testenv.SameJSON(got[1].Body, `{"Host":"x"}`) {
		t.Errorf("next calls = %v", got)
	}
	if m.main.Running("c1") || !m.next.Running("c1") {
		t.Errorf("running: main %v, next %v", m.main.Running("c1"), m.next.Running("c1"))
	}
	m.has("moved c1 from main to next")
	m.lacks("slipped in")
}

func TestMoveNextToMain(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{}`)
	m.exec(`UPDATE connection SET liercd='next' WHERE id='c1'`)
	m.next.MarkRunning("c1")
	if err := m.run("move-connection", "c1", "main"); err != nil {
		t.Fatal(err)
	}
	want := "main GET /c1/status|next POST /c1/destroy|next POST /c1/destroy|main POST /c1/create"
	if got := m.calls(); got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if n := m.count(`SELECT count(*) FROM connection WHERE id='c1' AND liercd IS NULL`); n != 1 {
		t.Errorf("column isn't NULL after moving to main")
	}
	m.has("moved c1 from next to main")
}

func TestMoveRefusals(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{}`)
	for _, tc := range []struct {
		name string
		args []string
		pre  func()
		want string
	}{
		{"unknown", []string{"nope", "next"}, nil, "unknown connection nope"},
		{"already", []string{"c1", "main"}, nil, "c1 is already on main"},
		{"unset host", []string{"c1", "next"}, func() { m.env.Config.LiercdNextHost = "" }, "LIERCD_NEXT_HOST"},
	} {
		if tc.pre != nil {
			tc.pre()
		}
		err := m.run("move-connection", tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
		m.calledNothing()
		if got := m.column("c1"); got != "" {
			t.Errorf("%s: column = %q", tc.name, got)
		}
	}
}

func TestMoveUsage(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{}`)
	for _, args := range [][]string{{}, {"c1"}, {"c1", "next", "x"}, {"c1", "other"}} {
		var u *UsageError
		if err := m.run("move-connection", args...); !asUsage(err, &u) {
			t.Errorf("%v: err = %v, want UsageError", args, err)
		}
	}
	m.calledNothing()
}

func TestMoveDestroyFailureStops(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{}`)
	m.main.Respond("POST", "/c1/destroy", 500, "boom")
	if err := m.run("move-connection", "c1", "next"); err == nil {
		t.Fatal("want error")
	}
	if got := m.column("c1"); got != "" {
		t.Errorf("column = %q", got)
	}
	if got := m.calls(); got != "next GET /c1/status|main POST /c1/destroy" {
		t.Errorf("calls = %q", got)
	}
}

func TestMoveCreateFailure(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{}`)
	m.main.MarkRunning("c1")
	m.next.Respond("POST", "/c1/create", 500, "boom")
	err := m.run("move-connection", "c1", "next")
	if err == nil {
		t.Fatal("want error")
	}
	if got := m.column("c1"); got != "next" {
		t.Errorf("column = %q", got)
	}
	m.has("c1 is now on next, but creating it there failed: ", "; it will be created when its user's stream next opens, or run lctl reconnect c1")
}

// A stream opening while the move runs recreates the connection on the old
// instance (the column still says so); the second destroy removes it.
func TestMoveRecreateSlipsIn(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{}`)
	m.main.MarkRunning("c1")
	destroys := 0
	m.after = func(name, method, path string) {
		if name == "main" && method == "POST" && path == "/c1/destroy" {
			if destroys++; destroys == 1 {
				m.main.MarkRunning("c1") // the stream's POST create on main
			}
		}
	}
	if err := m.run("move-connection", "c1", "next"); err != nil {
		t.Fatal(err)
	}
	want := "next GET /c1/status|main POST /c1/destroy|main POST /c1/destroy|next POST /c1/create"
	if got := m.calls(); got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if m.main.Running("c1") {
		t.Error("still running on main")
	}
	if !m.next.Running("c1") {
		t.Error("not created on next")
	}
	if got := m.column("c1"); got != "next" {
		t.Errorf("column = %q", got)
	}
	m.has("a recreate on main slipped in during the move; destroyed it there\n", "moved c1 from main to next")
}

func TestMoveSecondDestroyFailure(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{}`)
	destroys := 0
	m.after = func(name, method, path string) {
		if name == "main" && path == "/c1/destroy" {
			if destroys++; destroys == 1 {
				m.main.Respond("POST", "/c1/destroy", 500, "boom")
			}
		}
	}
	if err := m.run("move-connection", "c1", "next"); err == nil {
		t.Fatal("want error")
	}
	if m.next.Running("c1") {
		t.Error("created on next after a failed second destroy")
	}
	m.has("c1 is now on next, but destroying it again on main failed: liercd said 500 Internal Server Error")
}

func TestMoveTargetUnreachable(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{}`)
	m.main.MarkRunning("c1")
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	m.env.Liercd = liercd.NewRoutedForTest(m.mainURL, gone.URL, m.db.LookupLiercd)
	err := m.run("move-connection", "c1", "next")
	if err == nil || !strings.HasPrefix(err.Error(), "liercd instance next isn't reachable: ") {
		t.Fatalf("err = %v", err)
	}
	m.calledNothing()
	if !m.main.Running("c1") {
		t.Error("destroyed on main")
	}
	if got := m.column("c1"); got != "" {
		t.Errorf("column = %q", got)
	}
}

func TestMoveTargetAlreadyRunning(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{}`)
	m.main.MarkRunning("c1")
	m.next.MarkRunning("c1")
	err := m.run("move-connection", "c1", "next")
	if err == nil || err.Error() != "c1 is already running on next" {
		t.Fatalf("err = %v", err)
	}
	if got := m.calls(); got != "next GET /c1/status" {
		t.Errorf("calls = %q", got)
	}
	if !m.main.Running("c1") {
		t.Error("destroyed on main")
	}
	if got := m.column("c1"); got != "" {
		t.Errorf("column = %q", got)
	}
}

// A connection that isn't running stays dormant: the column moves, but
// nothing is created on the target; it starts there when its user's stream
// next opens.
func TestMoveDormant(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{}`)
	if err := m.run("move-connection", "c1", "next"); err != nil {
		t.Fatal(err)
	}
	want := "next GET /c1/status|main POST /c1/destroy|main POST /c1/destroy"
	if got := m.calls(); got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if got := m.column("c1"); got != "next" {
		t.Errorf("column = %q", got)
	}
	if m.next.Running("c1") {
		t.Error("started on next")
	}
	m.has("moved c1 from main to next; it wasn't running, so it will start there when its user's stream next opens\n")
}

// A stream that opens during the move of a dormant connection makes it
// live: the second destroy finds it running on the old instance, so it is
// created on the new one.
func TestMoveDormantRecreateSlipsIn(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{}`)
	destroys := 0
	m.after = func(name, method, path string) {
		if name == "main" && method == "POST" && path == "/c1/destroy" {
			if destroys++; destroys == 1 {
				m.main.MarkRunning("c1")
			}
		}
	}
	if err := m.run("move-connection", "c1", "next"); err != nil {
		t.Fatal(err)
	}
	if m.main.Running("c1") || !m.next.Running("c1") {
		t.Errorf("running: main %v, next %v", m.main.Running("c1"), m.next.Running("c1"))
	}
	m.has("slipped in", "moved c1 from main to next\n")
	m.lacks("wasn't running")
}
