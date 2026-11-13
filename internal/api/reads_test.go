package api

import (
	"context"
	"io"
	"net/url"
	"reflect"
	"testing"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

// getJSON fetches path as user, failing unless it's Go's JSON.
func (e *env) getJSON(path, user string) (int, string) {
	e.t.Helper()
	res := e.do("GET", path, user)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.Header.Get("Content-Type") != "application/javascript;charset=utf-8" {
		e.t.Errorf("GET %s: content type %q", path, res.Header.Get("Content-Type"))
	}
	return res.StatusCode, string(b)
}

func (e *env) want(path, user string, code int, body string) {
	e.t.Helper()
	if c, b := e.getJSON(path, user); c != code || b != body {
		e.t.Errorf("GET %s: %d %s, want %d %s", path, c, b, code, body)
	}
}

var readPaths = []string{"/preference", "/privates", "/seen", "/missed", "/notification/web_push_keys", "/connection/c1/channel"}

func TestReadsNeedLogin(t *testing.T) {
	e := setup(t)
	for _, p := range readPaths {
		e.want(p, "", 401, `{"status":"error","error":"unauthorized"}`)
	}
}

func TestPreference(t *testing.T) {
	e := setup(t)
	a, c := testenv.AddUser(t, e.db, "alice"), testenv.AddUser(t, e.db, "carol")
	e.exec(`INSERT INTO pref ("user", name, value) VALUES ($1,'theme','"dark"')`, a)
	e.want("/preference", a, 200, `[{"name":"theme","value":"\"dark\""}]`)
	e.want("/preference", c, 200, `[]`)
}

func TestPrivatesRoute(t *testing.T) {
	e := setup(t)
	a, c := testenv.AddUser(t, e.db, "alice"), testenv.AddUser(t, e.db, "carol")
	testenv.AddConnection(t, e.db, "c1", a, "{}")
	e.exec(`INSERT INTO private (connection, nick, time) VALUES ('c1','bob','2026-10-29 12:34:56.789')`)
	e.want("/privates", a, 200, `[{"nick":"bob","connection":"c1","time":"2026-10-29 12:34:56.789"}]`)
	e.want("/privates", c, 200, `[]`)
}

func TestSeenRoute(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	testenv.AddConnection(t, e.db, "c1", a, "{}")
	e.exec(`INSERT INTO seen ("user", connection, channel, message_id) VALUES ($1,'c1','#a',7)`, a)
	e.want("/seen", a, 200, `[{"connection":"c1","channel":"#a","message_id":7}]`)
}

func TestWebPushKeys(t *testing.T) {
	e := setupCfg(t, config.Config{AccountTokenSecret: testSecret, VAPIDPublic: "pk"})
	a := testenv.AddUser(t, e.db, "alice")
	e.want("/notification/web_push_keys", a, 200, `{"vapid_public_key":"pk"}`)

	e = setup(t)
	a = testenv.AddUser(t, e.db, "alice")
	e.want("/notification/web_push_keys", a, 200, `{"vapid_public_key":null}`)
}

func logRow(e *env, id int64, conn, user, channel string, command any, highlight bool) {
	e.t.Helper()
	e.exec(`INSERT INTO log (id, connection, "user", channel, command, highlight, time, message) VALUES ($1,$2,$3,$4,$5,$6,now(),'{}')`,
		id, conn, user, channel, command, highlight)
}

func TestChannelList(t *testing.T) {
	e := setup(t)
	a, b := testenv.AddUser(t, e.db, "alice"), testenv.AddUser(t, e.db, "bob")
	testenv.AddConnection(t, e.db, "c1", a, "{}")
	logRow(e, 1, "c1", a, "#b", "PRIVMSG", false)
	logRow(e, 2, "c1", a, "#a", "PRIVMSG", false)
	e.want("/connection/c1/channel", a, 200, `["#a","#b"]`)
	e.want("/connection/c1/channel", b, 401, `{"status":"error","error":"Invalid connection id 'c1'"}`)
	e.want("/connection/nope/channel", a, 401, `{"status":"error","error":"Invalid connection id 'nope'"}`)
}

func TestMissedRoute(t *testing.T) {
	e := setup(t)
	a, b := testenv.AddUser(t, e.db, "alice"), testenv.AddUser(t, e.db, "bob")
	testenv.AddConnection(t, e.db, "c1", a, "{}")
	testenv.AddConnection(t, e.db, "bobconn", b, "{}")
	logRow(e, 1, "c1", a, "#a", "PRIVMSG", false)
	logRow(e, 2, "c1", a, "#a", "PRIVMSG", true)
	logRow(e, 3, "c1", a, "#a", "JOIN", false)
	logRow(e, 4, "c1", a, "#a", nil, false)
	logRow(e, 5, "c1", a, "#foo-bar", "PRIVMSG", false)
	logRow(e, 6, "bobconn", b, "#a", "PRIVMSG", false)

	e.want("/missed", a, 200, `{}`)
	one := `{"c1":{"#a":{"messages":1,"events":1,"highlights":1}}}`
	e.want("/missed?c1-%23a=1", a, 200, one)
	e.want("/missed?c1-%23a=1&c1-%23foo-bar=0", a, 200,
		`{"c1":{"#a":{"messages":1,"events":1,"highlights":1},"#foo-bar":{"messages":1,"events":0,"highlights":0}}}`)
	e.want("/missed?c1-%23a=1&bobconn-%23a=0&x=1&c1-%23a2=abc", a, 200, one)
}

func TestParseMissed(t *testing.T) {
	q, _ := url.ParseQuery("c1-%23a=5&c1-%23b=1&c1-%23b=9&noDash=1&c1-%23c=x")
	got := map[db.MissedEntry]bool{}
	for _, m := range parseMissed(q) {
		got[m] = true
	}
	want := map[db.MissedEntry]bool{{Connection: "c1", Channel: "#a", After: 5}: true, {Connection: "c1", Channel: "#b", After: 9}: true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseMissed = %v", got)
	}
}

func TestMovedPathsOtherMethodsNotFound(t *testing.T) {
	e := setup(t)
	for _, rq := range [][2]string{
		{"POST", "/privates"}, {"POST", "/missed"}, {"POST", "/connection/c1/channel"},
		{"DELETE", "/preference/theme"}, {"PUT", "/connection/c1/nick/bob"}, {"GET", "/connection/c1/channel/%23a/seen"},
		{"PUT", "/notification/web_push"},
	} {
		checkNotFound(t, e, rq[0], rq[1], "")
	}
}
