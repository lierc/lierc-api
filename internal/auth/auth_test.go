package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/login"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

const (
	secret       = "testsecret"
	unauthorized = `{"status":"error","error":"unauthorized"}`
	unverified   = `{"status":"error","error":"Confirm your email address first","unverified":true}`
	// perl is session.WritePerl("perlvictim0000000000aa", "testsecret",
	// "1792769372.78598"), a Perl-format cookie made before the Perl code was
	// deleted
	perl = "1792769372.78598%3ABQsDAAAAAQoWcGVybHZpY3RpbTAwMDAwMDAwMDBhYQAAAAR1c2Vy%3A954572711b8433fb5cc3b3b98fb26f6d1f58ba5a"
)

// run serves one request through users.Require (or RequireAny) around a
// handler that writes the user; cookie "" with has false sends no cookie.
func run(users Users, any bool, cookie *string, tweak func(*http.Request)) *httptest.ResponseRecorder {
	hf := func(w http.ResponseWriter, r *http.Request, user string) { io.WriteString(w, "user="+user) }
	h := users.Require(hf)
	if any {
		h = users.RequireAny(hf)
	}
	req := httptest.NewRequest("GET", "/", nil)
	if cookie != nil {
		req.AddCookie(&http.Cookie{Name: "chats", Value: *cookie})
	}
	if tweak != nil {
		tweak(req)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func serve(t *testing.T, d *db.DB, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	return run(Users{DB: d}, false, &cookie, nil)
}

func setCookies(w *httptest.ResponseRecorder) []*http.Cookie {
	return (&http.Response{Header: w.Header()}).Cookies()
}

func setLastUsed(t *testing.T, d *db.DB, token string, ago time.Duration) {
	t.Helper()
	l, ok, err := login.Lookup(context.Background(), d, token)
	if err != nil || !ok {
		t.Fatalf("lookup: %v %v", ok, err)
	}
	if _, err := d.Pool.Exec(context.Background(), `UPDATE login SET last_used = now() - $2::interval WHERE id=$1`, l.ID, ago.String()); err != nil {
		t.Fatal(err)
	}
}

func lastUsed(t *testing.T, d *db.DB, token string) time.Time {
	t.Helper()
	l, ok, err := login.Lookup(context.Background(), d, token)
	if err != nil || !ok {
		t.Fatalf("lookup: %v %v", ok, err)
	}
	return l.LastUsed
}

func TestRequireToken(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "alice")
	w := serve(t, d, testenv.Login(t, d, u))
	if w.Code != 200 || w.Body.String() != "user="+u {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	if c := w.Header()["Set-Cookie"]; len(c) != 0 {
		t.Errorf("fresh login set a cookie: %v", c)
	}
}

func TestRequireRenews(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "alice")
	token := testenv.Login(t, d, u)

	setLastUsed(t, d, token, 2*time.Hour)
	w := run(Users{DB: d, Secure: true}, false, &token, nil)
	cs := setCookies(w)
	if w.Code != 200 || len(cs) != 1 || cs[0].Name != "chats" || cs[0].Value != token || !cs[0].Secure {
		t.Fatalf("%d %v", w.Code, w.Header()["Set-Cookie"])
	}
	if d := time.Since(lastUsed(t, d, token)); d > time.Second || d < -time.Second {
		t.Errorf("last_used %v from now", d)
	}

	setLastUsed(t, d, token, 30*time.Minute)
	before := lastUsed(t, d, token)
	w = serve(t, d, token)
	if w.Code != 200 || len(w.Header()["Set-Cookie"]) != 0 {
		t.Errorf("%d %v", w.Code, w.Header()["Set-Cookie"])
	}
	if got := lastUsed(t, d, token); !got.Equal(before) {
		t.Errorf("last_used changed: %v -> %v", before, got)
	}
}

func TestRequireBadCookies(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "alice")
	deleted := testenv.Login(t, d, u)
	if err := login.DeleteToken(context.Background(), d, deleted); err != nil {
		t.Fatal(err)
	}
	expired := testenv.Login(t, d, u)
	setLastUsed(t, d, expired, 8*24*time.Hour)
	for name, v := range map[string]string{
		"empty":   "",
		"x":       "x",
		"unknown": strings.Repeat("A", 43),
		"deleted": deleted,
		"expired": expired,
		"perl":    perl,
	} {
		w := serve(t, d, v)
		if w.Code != 401 || w.Body.String() != unauthorized {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
		cs := setCookies(w)
		if len(cs) != 1 || cs[0].Name != "chats" || cs[0].MaxAge >= 0 {
			t.Errorf("%s: cookie not expired: %v", name, w.Header()["Set-Cookie"])
		}
	}
	w := run(Users{DB: d}, false, nil, nil)
	if w.Code != 401 || w.Body.String() != unauthorized || len(w.Header()["Set-Cookie"]) != 0 {
		t.Errorf("no cookie: %d %s %v", w.Code, w.Body, w.Header()["Set-Cookie"])
	}
}

func TestRequireUnverified(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "newbie")
	token := testenv.Login(t, d, u)
	set := func(v any) {
		if _, err := d.Pool.Exec(context.Background(), `UPDATE "user" SET verified=$2 WHERE id=$1`, u, v); err != nil {
			t.Fatal(err)
		}
	}
	set(false)
	if w := serve(t, d, token); w.Code != 403 || w.Body.String() != unverified || len(w.Header()["Set-Cookie"]) != 0 {
		t.Errorf("unverified: %d %s %v", w.Code, w.Body, w.Header()["Set-Cookie"])
	}
	if w := run(Users{DB: d}, true, &token, nil); w.Code != 200 || w.Body.String() != "user="+u {
		t.Errorf("RequireAny unverified: %d %s", w.Code, w.Body)
	}
	for _, v := range []any{true, nil} {
		set(v)
		if w := serve(t, d, token); w.Code != 200 || w.Body.String() != "user="+u {
			t.Errorf("verified %v: %d %s", v, w.Code, w.Body)
		}
	}
}

func TestLoginIDInContext(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "alice")
	var got int64
	var ok bool
	users := Users{DB: d}
	h := users.Require(func(w http.ResponseWriter, r *http.Request, user string) { got, ok = LoginID(r.Context()) })
	do := func(v string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/", nil)
		req.AddCookie(&http.Cookie{Name: "chats", Value: v})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	token := testenv.Login(t, d, u)
	l, _, _ := login.Lookup(context.Background(), d, token)
	do(token)
	if !ok || got != l.ID {
		t.Errorf("token: %d %v, want %d", got, ok, l.ID)
	}
	if _, ok := LoginID(context.Background()); ok {
		t.Error("LoginID on a bare context")
	}
}

func TestRenewFailureLetsRequestThrough(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "alice")
	token := testenv.Login(t, d, u)
	setLastUsed(t, d, token, 2*time.Hour)
	users := Users{DB: d, touch: func(context.Context, int64) error { return errors.New("boom") }}
	w := run(users, false, &token, nil)
	if w.Code != 200 || w.Body.String() != "user="+u {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	if c := w.Header()["Set-Cookie"]; len(c) != 0 {
		t.Errorf("Set-Cookie %v", c)
	}
}

func TestClient(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("User-Agent", "ua")
	for addr, want := range map[string]string{"192.0.2.7:1234": "192.0.2.7", "[2001:db8::1]:80": "2001:db8::1", "192.0.2.8": "192.0.2.8"} {
		r.RemoteAddr = addr
		if ua, ip := Client(r); ua != "ua" || ip != want {
			t.Errorf("%s: %q %q", addr, ua, ip)
		}
	}
}

// A Perl-format cookie, correctly signed with the old API_SECRET, is
// refused like any other unknown value: no login is made for it, and the
// cookie is cleared.
func TestPerlCookieRefused(t *testing.T) {
	d := testenv.Postgres(t)
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO "user" (id, username, email, verified) VALUES ('perlvictim0000000000aa', 'perlvictim', 'p@example.com', true)`); err != nil {
		t.Fatal(err)
	}
	w := serve(t, d, perl)
	if w.Code != 401 || strings.TrimSpace(w.Body.String()) != unauthorized {
		t.Errorf("Perl cookie: %d %s, want 401", w.Code, w.Body)
	}
	if cs := setCookies(w); len(cs) != 1 || cs[0].Value != "" || cs[0].MaxAge >= 0 {
		t.Errorf("cookie not cleared: %v", cs)
	}
	var n int
	d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM login`).Scan(&n)
	if n != 0 {
		t.Errorf("%d login(s) made for a Perl cookie", n)
	}
}
