package api

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/testenv"
)

// send sends method path with body, logged in as user unless "".
func (e *env) send(method, path, user, body string, hdr ...string) (int, string) {
	e.t.Helper()
	res := e.doReq(method, path, user, body, hdr...)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.Header.Get("X-Perl") != "" {
		e.t.Errorf("%s %s reached Perl", method, path)
	}
	return res.StatusCode, string(b)
}

func (e *env) sendWant(method, path, user, body string, code int, want string) {
	e.t.Helper()
	if c, b := e.send(method, path, user, body); c != code || b != want {
		e.t.Errorf("%s %s %q: %d %s, want %d %s", method, path, body, c, b, code, want)
	}
}

func (e *env) row(sql string, args ...any) string {
	e.t.Helper()
	var v string
	if err := e.db.Pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatal(err)
	}
	return v
}

func TestWritesNeedLogin(t *testing.T) {
	e := setup(t)
	for _, rq := range [][2]string{
		{"POST", "/preference/theme"}, {"GET", "/unsubscribe"}, {"POST", "/connection/c1/channel/%23a/seen"},
		{"POST", "/connection/c1/nick/bob"}, {"DELETE", "/connection/c1/nick/bob"},
	} {
		e.sendWant(rq[0], rq[1], "", "1", 401, `{"status":"error","error":"unauthorized"}`)
	}
}

func TestSetPreference(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	e.sendWant("POST", "/preference/theme", a, `"dark"`, 204, "")
	if v := e.row(`SELECT value FROM pref WHERE "user"=$1 AND name='theme'`, a); v != `"dark"` {
		t.Errorf("theme = %s", v)
	}
	e.sendWant("POST", "/preference/"+strings.Repeat("a", 129), a, "1", 400, `{"status":"error","error":"preference name is too long"}`)
	e.sendWant("POST", "/preference/long", a, strings.Repeat("é", 513), 400, `{"status":"error","error":"preference value is too long"}`)
	e.sendWant("POST", "/preference/long", a, strings.Repeat("é", 512), 204, "")
	e.sendWant("POST", "/preference/huge", a, strings.Repeat("a", 65*1024), 400, `{"status":"error","error":"request body is too large"}`)
	e.sendWant("POST", "/preference/bad", a, "ab\xffc", 204, "")
	if v := e.row(`SELECT value FROM pref WHERE "user"=$1 AND name='bad'`, a); v != "ab�c" {
		t.Errorf("bad = %q", v)
	}
}

func TestUnsubscribe(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	e.sendWant("GET", "/unsubscribe", a, "", 204, "")
	if v := e.row(`SELECT value FROM pref WHERE "user"=$1 AND name='email'`, a); v != "false" {
		t.Errorf("email = %s", v)
	}
}

func TestSetSeenRoute(t *testing.T) {
	e := setup(t)
	a, b := testenv.AddUser(t, e.db, "alice"), testenv.AddUser(t, e.db, "bob")
	testenv.AddConnection(t, e.db, "c1", a, "{}")
	e.sendWant("POST", "/connection/c1/channel/%23Foo/seen", a, " 42 ", 204, "")
	if v := e.row(`SELECT channel || ' ' || message_id FROM seen WHERE "user"=$1`, a); v != "#foo 42" {
		t.Errorf("seen = %s", v)
	}
	e.sendWant("POST", "/connection/c1/channel/%23Foo/seen", a, "-1", 204, "")
	for _, body := range []string{"abc", "", "3000000000"} {
		e.sendWant("POST", "/connection/c1/channel/%23a/seen", a, body, 400, `{"status":"error","error":"message id must be a number"}`)
	}
	e.sendWant("POST", "/connection/c1/channel/%23"+strings.Repeat("a", 32)+"/seen", a, "1", 400, `{"status":"error","error":"channel name is too long"}`)
	e.sendWant("POST", "/connection/c1/channel/%23a/seen", b, "abc", 401, `{"status":"error","error":"Invalid connection id 'c1'"}`)
}

func TestPrivateRoutes(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	testenv.AddConnection(t, e.db, "c1", a, "{}")
	e.sendWant("POST", "/connection/c1/nick/%C3%A9Bob", a, "", 204, "")
	if v := e.row(`SELECT nick FROM private WHERE connection='c1'`); v != "éBob" {
		t.Errorf("nick = %q", v)
	}
	e.sendWant("DELETE", "/connection/c1/nick/%C3%A9Bob", a, "", 200, `{"status":"ok"}`)
	if v := e.row(`SELECT count(*)::text FROM private`); v != "0" {
		t.Errorf("%s rows left", v)
	}
	e.sendWant("DELETE", "/connection/c1/nick/%C3%A9Bob", a, "", 200, `{"status":"ok"}`)
	e.sendWant("POST", "/connection/c1/nick/"+strings.Repeat("n", 33), a, "", 400, `{"status":"error","error":"nick is too long"}`)
	e.sendWant("POST", "/connection/nope/nick/bob", a, "", 401, `{"status":"error","error":"Invalid connection id 'nope'"}`)
}

func TestWritesCrossSite(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	if c, _ := e.send("POST", "/preference/theme", a, `"x"`, "Sec-Fetch-Site", "cross-site"); c != 403 {
		t.Errorf("cross-site: %d, want 403", c)
	}
	if v := e.row(`SELECT count(*)::text FROM pref`); v != "0" {
		t.Errorf("%s prefs written", v)
	}
}
