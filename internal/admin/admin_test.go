package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/admin"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/login"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

type env struct {
	t *testing.T
	d *db.DB
	h http.Handler
}

func newEnv(t *testing.T) *env {
	d := testenv.Postgres(t)
	return &env{t, d, admin.New(d, nil)}
}

func (e *env) get(path string) (int, string) {
	e.t.Helper()
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w.Code, strings.TrimSpace(w.Body.String())
}

func (e *env) getJSON(path string, v any) {
	e.t.Helper()
	code, body := e.get(path)
	if code != 200 {
		e.t.Fatalf("GET %s: %d %s", path, code, body)
	}
	if err := json.Unmarshal([]byte(body), v); err != nil {
		e.t.Fatalf("GET %s: %v: %s", path, err, body)
	}
}

func (e *env) exec(sql string) {
	e.t.Helper()
	if _, err := e.d.Pool.Exec(context.Background(), sql); err != nil {
		e.t.Fatal(err)
	}
}

func TestListUsers(t *testing.T) {
	e := newEnv(t)
	a := testenv.AddUser(t, e.d, "alice")
	b := testenv.AddUser(t, e.d, "bob")
	testenv.AddConnection(t, e.d, "c1", a, `{}`)
	e.exec(`UPDATE "user" SET last_login = '2020-01-02 03:04:05', registered = '2019-01-01 00:00:00' WHERE id = '` + a + `'`)
	e.exec(`UPDATE "user" SET last_login = now() at time zone 'utc', registered = NULL, verified = NULL WHERE id = '` + b + `'`)

	var users []admin.User
	e.getJSON("/admin/users", &users)
	if len(users) != 2 || users[0].ID != a || users[1].ID != b {
		t.Fatalf("users = %+v", users)
	}
	u := users[0]
	if u.Username != "alice" || u.Email != "alice@example.com" || u.Verified == nil || !*u.Verified ||
		u.LastLogin == nil || u.LastLogin.Format("2006-01-02T15:04:05Z07:00") != "2020-01-02T03:04:05Z" || u.Connections != 1 {
		t.Errorf("alice = %+v", u)
	}
	if users[1].Registered != nil || users[1].Verified != nil {
		t.Errorf("bob = %+v", users[1])
	}
	_, body := e.get("/admin/users")
	if !strings.Contains(body, `"last_login":"2020-01-02T03:04:05Z"`) || !strings.Contains(body, `"registered":null`) || !strings.Contains(body, `"verified":null`) {
		t.Errorf("body = %s", body)
	}

	e.getJSON("/admin/users?inactive_since=2021-01-01T00:00:00Z", &users)
	if len(users) != 1 || users[0].ID != a {
		t.Errorf("inactive = %+v", users)
	}
	e.getJSON("/admin/users?ids="+b+",nosuch", &users)
	if len(users) != 1 || users[0].ID != b {
		t.Errorf("ids = %+v", users)
	}
	var empty []admin.User
	e.getJSON("/admin/users?ids=nosuch", &empty)
	if _, body := e.get("/admin/users?ids=nosuch"); body != "[]" {
		t.Errorf("unknown ids body = %q", body)
	}
}

func TestLookupUsersBoth(t *testing.T) {
	e := newEnv(t)
	a := testenv.AddUser(t, e.d, "alice")
	b := testenv.AddUser(t, e.d, "bob")
	e.exec(`ALTER TABLE "user" DROP CONSTRAINT user_username_valid`)
	e.exec(`UPDATE "user" SET username = 'ALICE@example.com' WHERE id = '` + b + `'`)
	var users []admin.User
	e.getJSON("/admin/users/lookup?q=alice@EXAMPLE.com", &users)
	got := map[string]bool{}
	for _, u := range users {
		got[u.ID] = true
	}
	if len(users) != 2 || !got[a] || !got[b] {
		t.Errorf("users = %+v", users)
	}
	if code, _ := e.get("/admin/users/lookup"); code != 400 {
		t.Errorf("no q: %d", code)
	}
}

func TestUserDetail(t *testing.T) {
	e := newEnv(t)
	a := testenv.AddUser(t, e.d, "alice") // no password
	b := testenv.AddUser(t, e.d, "bob")
	e.exec(`UPDATE "user" SET password_hash = 'h' WHERE id = '` + b + `'`)
	testenv.AddConnection(t, e.d, "c1", a, `{"Host":"h.example","Port":6667}`)
	for id, want := range map[string]string{a: "none", b: "bcrypt"} {
		var d admin.UserDetail
		e.getJSON("/admin/users/"+id, &d)
		if d.ID != id || d.Password != want {
			t.Errorf("%s: %+v", id, d)
		}
	}
	var d admin.UserDetail
	e.getJSON("/admin/users/"+a, &d)
	if len(d.ConnectionList) != 1 || d.ConnectionList[0].ID != "c1" || d.ConnectionList[0].Host != "h.example" ||
		d.ConnectionList[0].Port != 6667 || d.Connections != 1 {
		t.Errorf("detail = %+v", d)
	}
	_, body := e.get("/admin/users/" + b)
	if !strings.Contains(body, `"connection_list":[]`) || strings.Contains(body, "hash") {
		t.Errorf("body = %s", body)
	}
	code, body := e.get("/admin/users/nosuchuser")
	if code != 404 || body != `{"error":"no such user"}` {
		t.Errorf("unknown: %d %s", code, body)
	}
	if code, _ := e.get("/admin/users/nosuchuser/counts"); code != 404 {
		t.Errorf("counts unknown: %d", code)
	}
}

func TestUserCounts(t *testing.T) {
	e := newEnv(t)
	a := testenv.AddUser(t, e.d, "alice")
	testenv.AddConnection(t, e.d, "c1", a, `{}`)
	testenv.AddLog(t, e.d, 1, "c1", a)
	e.exec(`INSERT INTO pref ("user", name, value) VALUES ('` + a + `', 'p', 'v')`)
	var got map[string]int64
	e.getJSON("/admin/users/"+a+"/counts", &got)
	want := map[string]int64{"log": 1, "log_archive": 0, "seen": 0, "private": 0, "pref": 1, "web_push": 0, "connection": 1, "login": 0}
	if len(got) != len(want) {
		t.Fatalf("counts = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d", k, got[k], v)
		}
	}
}

// The view is the connection alone: counting its rows is a separate call,
// so routing lookups (which only need the instance) don't pay for counts.
func TestConnectionView(t *testing.T) {
	e := newEnv(t)
	a := testenv.AddUser(t, e.d, "alice")
	testenv.AddConnection(t, e.d, "c1", a, `{"Host":"h","Port":1}`)
	testenv.AddLog(t, e.d, 1, "c1", a)
	code, body := e.get("/admin/connections/c1")
	if code != 200 || strings.Contains(body, `"counts"`) {
		t.Errorf("view: %d %s, want no counts", code, body)
	}
	var c admin.Connection
	e.getJSON("/admin/connections/c1", &c)
	if c.ID != "c1" || c.User != a || c.Username != "alice" || c.Instance != "main" {
		t.Errorf("view = %+v", c)
	}
	code, body = e.get("/admin/connections/nosuch")
	if code != 404 || body != `{"error":"no such connection"}` {
		t.Errorf("unknown: %d %s", code, body)
	}
}

func TestConnectionCounts(t *testing.T) {
	e := newEnv(t)
	a := testenv.AddUser(t, e.d, "alice")
	testenv.AddConnection(t, e.d, "c1", a, `{"Host":"h","Port":1}`)
	testenv.AddLog(t, e.d, 1, "c1", a)
	testenv.AddLog(t, e.d, 2, "c1", a)
	e.exec(`INSERT INTO private (connection, nick, time) VALUES ('c1', 'n', now())`)
	var got map[string]int64
	e.getJSON("/admin/connections/c1/counts", &got)
	if got["log"] != 2 || got["private"] != 1 || got["seen"] != 0 || got["log_archive"] != 0 || len(got) != 4 {
		t.Errorf("counts = %v", got)
	}
	code, body := e.get("/admin/connections/nosuch/counts")
	if code != 404 || body != `{"error":"no such connection"}` {
		t.Errorf("unknown: %d %s", code, body)
	}
}

func TestListConnections(t *testing.T) {
	e := newEnv(t)
	old := testenv.AddUser(t, e.d, "old")
	recent := testenv.AddUser(t, e.d, "recent")
	e.exec(`UPDATE "user" SET last_login = '2020-01-01' WHERE id = '` + old + `'`)
	testenv.AddConnection(t, e.d, "r1", recent, `{"Host":"r.example","Port":6667}`)
	testenv.AddConnection(t, e.d, "o2", old, `{"Host":"o.example","Port":6697}`)
	testenv.AddConnection(t, e.d, "o1", old, `{}`)
	e.exec(`UPDATE connection SET liercd = 'next' WHERE id = 'o2'`)
	e.exec(`UPDATE connection SET liercd = '' WHERE id = 'r1'`)

	var cs []admin.Connection
	e.getJSON("/admin/connections", &cs)
	if len(cs) != 3 || cs[0].ID != "o1" || cs[1].ID != "o2" || cs[2].ID != "r1" {
		t.Fatalf("conns = %+v", cs)
	}
	o2 := cs[1]
	if o2.User != old || o2.Username != "old" || o2.Instance != "next" || o2.Host != "o.example" || o2.Port != 6697 {
		t.Errorf("o2 = %+v", o2)
	}
	if cs[0].Instance != "main" || cs[2].Instance != "main" {
		t.Errorf("instances = %q %q", cs[0].Instance, cs[2].Instance)
	}
	var cfg map[string]any
	if err := json.Unmarshal(o2.Config, &cfg); err != nil || cfg["Host"] != "o.example" {
		t.Errorf("config = %s %v", o2.Config, err)
	}
	e.getJSON("/admin/connections?instance=next", &cs)
	if len(cs) != 1 || cs[0].ID != "o2" {
		t.Errorf("next = %+v", cs)
	}
	e.getJSON("/admin/connections?instance=main", &cs)
	if len(cs) != 2 {
		t.Errorf("main = %+v", cs)
	}
	e.getJSON("/admin/connections?inactive_since=2021-01-01T00:00:00Z", &cs)
	if len(cs) != 2 || cs[0].ID != "o1" || cs[1].ID != "o2" {
		t.Errorf("inactive = %+v", cs)
	}
}

func TestConnectionPasswordRedacted(t *testing.T) {
	e := newEnv(t)
	a := testenv.AddUser(t, e.d, "alice")
	testenv.AddConnection(t, e.d, "c1", a, `{"Host":"h","Port":1,"Pass":"hunter2"}`)
	testenv.AddConnection(t, e.d, "c2", a, `{"Host":"h","Port":1,"Pass":""}`)
	testenv.AddConnection(t, e.d, "c3", a, `{"Host":"h","Port":1}`)
	// a config that isn't an object (a double-encoded one) must not leak either
	testenv.AddConnection(t, e.d, "c4", a, `{}`)
	e.exec(`UPDATE connection SET config = '"{\"Pass\":\"hunter2\"}"'::jsonb WHERE id = 'c4'`)
	for _, path := range []string{"/admin/connections", "/admin/connections/c1", "/admin/users/" + a} {
		code, body := e.get(path)
		if code != 200 || strings.Contains(body, "hunter2") || !strings.Contains(body, `"Pass":"<set>"`) {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
	var cs []admin.Connection
	e.getJSON("/admin/connections", &cs)
	for _, c := range cs {
		var cfg map[string]any
		json.Unmarshal(c.Config, &cfg)
		pass, has := cfg["Pass"]
		switch c.ID {
		case "c1":
			if pass != "<set>" {
				t.Errorf("c1 Pass = %v", pass)
			}
		case "c2":
			if pass != "" {
				t.Errorf("c2 Pass = %v", pass)
			}
		case "c3":
			if has {
				t.Errorf("c3 gained Pass = %v", pass)
			}
		}
	}
	code, body := e.get("/admin/connections/c1/config")
	if code != 200 || !strings.Contains(body, "hunter2") {
		t.Errorf("/config: %d %s", code, body)
	}
	if code, body := e.get("/admin/connections/c4"); code != 200 || !strings.Contains(body, `"config":null`) {
		t.Errorf("c4 view: %d %s", code, body)
	}
	if code, body := e.get("/admin/connections/c4/config"); code != 200 || body != `"{\"Pass\":\"hunter2\"}"` {
		t.Errorf("c4 /config: %d %s", code, body)
	}
	if code, _ := e.get("/admin/connections/nosuch/config"); code != 404 {
		t.Errorf("/config unknown: %d", code)
	}
}

func TestLoginsList(t *testing.T) {
	e := newEnv(t)
	a := testenv.AddUser(t, e.d, "alice")
	if _, err := login.Create(context.Background(), e.d, a, "agent/1", "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	var got []struct {
		ID        int64  `json:"id"`
		Created   string `json:"created"`
		LastUsed  string `json:"last_used"`
		IP        string `json:"ip"`
		UserAgent string `json:"user_agent"`
	}
	e.getJSON("/admin/users/"+a+"/logins", &got)
	if len(got) != 1 || got[0].ID == 0 || got[0].IP != "192.0.2.1" || got[0].UserAgent != "agent/1" ||
		!strings.HasSuffix(got[0].Created, "Z") || !strings.HasSuffix(got[0].LastUsed, "Z") {
		t.Errorf("logins = %+v", got)
	}
	if code, _ := e.get("/admin/users/nosuch/logins"); code != 404 {
		t.Errorf("unknown user: %d", code)
	}
}

func TestLastLogID(t *testing.T) {
	e := newEnv(t)
	if _, body := e.get("/admin/log/last"); body != `{"id":0}` {
		t.Errorf("empty: %s", body)
	}
	a := testenv.AddUser(t, e.d, "alice")
	testenv.AddConnection(t, e.d, "c1", a, `{}`)
	testenv.AddLog(t, e.d, 42, "c1", a)
	if _, body := e.get("/admin/log/last"); body != `{"id":42}` {
		t.Errorf("got %s", body)
	}
}

func TestStaleSeen(t *testing.T) {
	e := newEnv(t)
	a := testenv.AddUser(t, e.d, "alice")
	testenv.AddConnection(t, e.d, "c1", a, `{}`)
	e.exec(`INSERT INTO seen ("user", connection, channel, message_id) VALUES ('` + a + `', 'c1', '#a', 1), ('` + a + `', 'gone', '#a', 1)`)
	if _, body := e.get("/admin/seen/stale"); body != `{"count":1}` {
		t.Errorf("got %s", body)
	}
}

func TestBadParams(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{
		"/admin/users?inactive_since=junk",
		"/admin/connections?inactive_since=junk",
		"/admin/connections?instance=x",
	} {
		code, body := e.get(path)
		if code != 400 || !strings.HasPrefix(body, `{"error":"`) {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
	for _, path := range []string{"/admin/users/nosuch", "/admin/connections/nosuch", "/admin/connections/nosuch/config", "/admin/nosuch"} {
		if code, _ := e.get(path); code != 404 {
			t.Errorf("%s: %d", path, code)
		}
	}
}
