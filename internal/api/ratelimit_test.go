package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/testenv"
)

const tooMany = `{"status":"error","error":"Too many login attempts — try again in a few minutes"}`

func (e *env) tryLogin(name, pass string) (*http.Response, string) {
	e.t.Helper()
	res, body := e.form("POST", "/auth", "", url.Values{"email": {name}, "pass": {pass}})
	return res, strings.TrimSpace(body)
}

// failLogins sends n wrong passwords for name, each wanting a 401.
func (e *env) failLogins(name string, n int) {
	e.t.Helper()
	for i := 0; i < n; i++ {
		if res, body := e.tryLogin(name, "wrong"); res.StatusCode != 401 || body != `{"status":"error","error":"Invalid email or password"}` {
			e.t.Fatalf("wrong password %d for %q: %d %s", i+1, name, res.StatusCode, body)
		}
	}
}

func (e *env) wantLimited(name, pass string) {
	e.t.Helper()
	res, body := e.tryLogin(name, pass)
	if res.StatusCode != 429 || body != tooMany || chatsCookie(res) != nil {
		e.t.Fatalf("login %q: %d %s, want 429 %s", name, res.StatusCode, body, tooMany)
	}
	n, err := strconv.Atoi(res.Header.Get("Retry-After"))
	if err != nil || n < 1 || n > 900 {
		e.t.Errorf("Retry-After %q, want 1..900", res.Header.Get("Retry-After"))
	}
}

func TestLoginLimit(t *testing.T) {
	e := setup(t)
	e.setPassword(testenv.AddUser(t, e.db, "alice"), "hunter2")
	e.failLogins("alice", 10)
	e.wantLimited("alice", "wrong")
	e.wantLimited("alice", "hunter2")
}

func TestLoginLimitCaseInsensitive(t *testing.T) {
	e := setup(t)
	e.setPassword(testenv.AddUser(t, e.db, "alice"), "hunter2")
	e.failLogins("alice", 5)
	e.failLogins(" ALICE ", 5)
	e.wantLimited("alice", "hunter2")
}

func TestLoginLimitUnknownAccount(t *testing.T) {
	e := setup(t)
	e.failLogins("nobody", 10)
	e.wantLimited("nobody", "wrong")
}

func TestLoginSuccessNotCounted(t *testing.T) {
	e := setup(t)
	e.setPassword(testenv.AddUser(t, e.db, "alice"), "hunter2")
	e.failLogins("alice", 9)
	if res, body := e.tryLogin("alice", "hunter2"); res.StatusCode != 200 {
		t.Fatalf("right password: %d %s", res.StatusCode, body)
	}
	e.failLogins("alice", 1)
}

func TestLoginLimitPerAccount(t *testing.T) {
	e := setup(t)
	e.setPassword(testenv.AddUser(t, e.db, "alice"), "hunter2")
	e.setPassword(testenv.AddUser(t, e.db, "bob"), "s3cret")
	e.failLogins("alice", 10)
	if res, body := e.tryLogin("bob", "s3cret"); res.StatusCode != 200 {
		t.Errorf("bob: %d %s", res.StatusCode, body)
	}
}

func TestLoginLimitConcurrent(t *testing.T) {
	e := setup(t)
	e.setPassword(testenv.AddUser(t, e.db, "alice"), "hunter2")
	var mu sync.Mutex
	codes := map[int]int{}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := http.PostForm(e.srv.URL+"/auth", url.Values{"email": {"alice"}, "pass": {"wrong"}})
			if err != nil {
				t.Error(err)
				return
			}
			res.Body.Close()
			mu.Lock()
			codes[res.StatusCode]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if codes[401] > 10 || codes[401]+codes[429] != 30 {
		t.Errorf("codes %v, want at most 10 401s and the rest 429", codes)
	}
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.mail)
}

func (e *env) reset(email string) {
	e.t.Helper()
	res, body := e.form("POST", "/reset", "", url.Values{"email": {email}})
	if res.StatusCode != 200 || strings.TrimSpace(body) != `{"status":"ok"}` {
		e.t.Fatalf("reset %q: %d %s", email, res.StatusCode, body)
	}
}

// noMoreMail waits a moment and checks the recorder still has n emails.
func noMoreMail(t *testing.T, rec *recorder, n int) {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	if c := rec.count(); c != n {
		t.Errorf("%d emails, want %d", c, n)
	}
}

func TestResetLimit(t *testing.T) {
	e, rec := mailEnv(t)
	testenv.AddUser(t, e.db, "alice")
	testenv.AddUser(t, e.db, "bob")
	for i, addr := range []string{"alice@example.com", "Alice@Example.com", "ALICE@example.com"} {
		e.reset(addr)
		rec.wait(t, i+1)
	}
	e.reset("alice@example.com")
	noMoreMail(t, rec, 3)
	e.reset("bob@example.com")
	if m := rec.wait(t, 4); m.to != "bob@example.com" {
		t.Errorf("4th email to %s, want bob", m.to)
	}
}

func TestResetLimitUnknownAddress(t *testing.T) {
	e, rec := mailEnv(t)
	for i := 0; i < 3; i++ {
		e.reset("later@example.com")
	}
	id := testenv.AddUser(t, e.db, "later")
	if _, err := e.db.Pool.Exec(context.Background(), `UPDATE "user" SET email = 'later@example.com' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	e.reset("later@example.com")
	noMoreMail(t, rec, 0)
}

func TestResetBadEmailNotCounted(t *testing.T) {
	e, rec := mailEnv(t)
	testenv.AddUser(t, e.db, "alice")
	for i := 0; i < 5; i++ {
		res, body := e.form("POST", "/reset", "", url.Values{"email": {"junk"}})
		if res.StatusCode != 400 {
			t.Fatalf("junk reset: %d %s", res.StatusCode, body)
		}
	}
	e.reset("alice@example.com")
	rec.wait(t, 1)
}
