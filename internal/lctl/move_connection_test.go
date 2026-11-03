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
	main, next *testenv.LiercdServer
	mu         sync.Mutex
	order      []string
}

func newMoveHarness(t *testing.T) *moveHarness {
	m := &moveHarness{harness: newHarness(t), main: testenv.NewLiercd(), next: testenv.NewLiercd()}
	serve := func(name string, f *testenv.LiercdServer) string {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.mu.Lock()
			m.order = append(m.order, name+" "+r.Method+" "+r.URL.Path)
			m.mu.Unlock()
			f.ServeHTTP(w, r)
		}))
		t.Cleanup(s.Close)
		return s.URL
	}
	mu, nu := serve("main", m.main), serve("next", m.next)
	m.env.Liercd = liercd.NewRoutedForTest(mu, nu, m.db.LookupLiercd)
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

func (m *moveHarness) calledNothing() {
	m.t.Helper()
	if len(m.order) != 0 {
		m.t.Errorf("liercd was called: %v", m.order)
	}
}

func TestMoveMainToNext(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{"Host":"x"}`)
	if err := m.run("move-connection", "c1", "next"); err != nil {
		t.Fatal(err)
	}
	want := []string{"main POST /c1/destroy", "next POST /c1/create"}
	if strings.Join(m.order, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %v, want %v", m.order, want)
	}
	if got := m.column("c1"); got != "next" {
		t.Errorf("column = %q", got)
	}
	if got := m.next.Calls(); len(got) != 1 || got[0].Body != `{"Host":"x"}` {
		t.Errorf("next calls = %v", got)
	}
	m.has("moved c1 from main to next")
}

func TestMoveNextToMain(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{}`)
	m.exec(`UPDATE connection SET liercd='next' WHERE id='c1'`)
	if err := m.run("move-connection", "c1", "main"); err != nil {
		t.Fatal(err)
	}
	want := "next POST /c1/destroy|main POST /c1/create"
	if got := strings.Join(m.order, "|"); got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if got := m.column("c1"); got != "" {
		t.Errorf("column = %q", got)
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
	if len(m.next.Calls()) != 0 {
		t.Errorf("next was called: %v", m.next.Calls())
	}
}

func TestMoveCreateFailure(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("c1", m.user("alice"), `{}`)
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
