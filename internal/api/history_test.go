package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/archive"
	"github.com/lierc/lierc-api/internal/testenv"
)

// msg inserts a PRIVMSG log row with text.
func (e *env) msg(id int64, conn, user, channel, text string, self, highlight bool, when string) {
	e.t.Helper()
	m := fmt.Sprintf(`{"Command":"PRIVMSG","Params":[%q,%q]}`, channel, text)
	e.exec(`INSERT INTO log (id, connection, "user", channel, command, self, highlight, time, message) VALUES ($1,$2,$3,$4,'PRIVMSG',$5,$6,$7::timestamp,$8)`,
		id, conn, user, channel, self, highlight, when, m)
}

// msgIDs is the MessageIds of a JSON list body.
func msgIDs(t *testing.T, body string) []int64 {
	t.Helper()
	var ms []struct{ MessageId int64 }
	if err := json.Unmarshal([]byte(body), &ms); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	out := []int64{}
	for _, m := range ms {
		out = append(out, m.MessageId)
	}
	return out
}

func (e *env) wantIDs(path, user string, want ...int64) {
	e.t.Helper()
	c, b := e.getJSON(path, user)
	if c != 200 || fmt.Sprint(msgIDs(e.t, b)) != fmt.Sprint(want) {
		e.t.Errorf("GET %s: %d %v, want %v", path, c, b, want)
	}
}

var historyPaths = []string{"/log/1", "/highlight", "/highlight/5", "/connection/c1/channel/%23a/events",
	"/connection/c1/channel/%23a/events/5", "/connection/c1/channel/%23a/last"}

func TestHistoryNeedsLogin(t *testing.T) {
	e := setup(t)
	for _, p := range historyPaths {
		e.want(p, "", 401, `{"status":"error","error":"unauthorized"}`)
	}
}

func TestLimit(t *testing.T) {
	for _, c := range []struct {
		q    string
		want int
	}{{"", 50}, {"limit=abc", 50}, {"limit=0", 50}, {"limit=-5", 50}, {"limit=7", 7}, {"limit=999", 150}} {
		q, _ := url.ParseQuery(c.q)
		if got := limit(q, 50, 150); got != c.want {
			t.Errorf("%q: %d, want %d", c.q, got, c.want)
		}
	}
}

func TestLogRoute(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	testenv.AddConnection(t, e.db, "c1", a, "{}")
	e.exec(`INSERT INTO log (id, connection, "user", channel, command, time, message)
		SELECT g, 'c1', $1, '#a', 'PRIVMSG', now(), '{}' FROM generate_series(1, 251) g`, a)
	c, b := e.getJSON("/log/1", a)
	if ids := msgIDs(t, b); c != 200 || len(ids) != 250 || ids[0] != 2 || ids[249] != 251 {
		t.Errorf("250 newer: %d, %d rows", c, len(msgIDs(t, b)))
	}
	e.want("/log/0", a, 400, `{"status":"error","error":"Too many messages!"}`)
	e.want("/log/abc", a, 400, `{"status":"error","error":"event id must be a number"}`)
	e.want("/log/99999999999", a, 400, `{"status":"error","error":"event id must be a number"}`)
}

func TestLogRouteBody(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	testenv.AddConnection(t, e.db, "c1", a, "{}")
	e.exec(`INSERT INTO log (id, connection, "user", channel, command, self, highlight, time, message) VALUES (7,'c1',$1,'#a','PRIVMSG',true,false,now(),'{"Command":"PRIVMSG"}')`, a)
	c, b := e.getJSON("/log/1", a)
	var got bytes.Buffer
	json.Compact(&got, []byte(b))
	if want := `[{"MessageId":7,"Message":{"Command":"PRIVMSG"},"ConnectionId":"c1","Self":true,"Highlight":false}]`; c != 200 || got.String() != want {
		t.Errorf("body = %d %s, want %s", c, got.String(), want)
	}
}

func TestHighlightRoutes(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	testenv.AddConnection(t, e.db, "c1", a, "{}")
	for i := int64(1); i <= 6; i++ {
		e.msg(i, "c1", a, "#a", "x", false, i%2 == 0, "2026-01-01")
	}
	e.wantIDs("/highlight", a, 6, 4, 2)
	e.wantIDs("/highlight/6", a, 4, 2)
	e.wantIDs("/highlight?limit=1", a, 6)
	e.want("/highlight/x", a, 400, `{"status":"error","error":"event id must be a number"}`)
}

func TestChannelEventsRoutes(t *testing.T) {
	e := setup(t)
	a, b := testenv.AddUser(t, e.db, "alice"), testenv.AddUser(t, e.db, "bob")
	testenv.AddConnection(t, e.db, "c1", a, "{}")
	for i := int64(1); i <= 6; i++ {
		e.msg(i, "c1", a, "#a", "x", false, false, "2020-01-01")
	}
	o := archive.Options{Keep: 2, Block: 2, MinAge: time.Hour, Now: time.Now()}
	for n := 0; n < 10; n++ {
		if _, ok, err := archive.MoveBlock(context.Background(), e.db, archive.Channel{Connection: "c1", Channel: "#a"}, o); err != nil || !ok {
			break
		}
	}
	e.wantIDs("/connection/c1/channel/%23A/events?limit=5", a, 6, 5, 4, 3, 2)
	e.wantIDs("/connection/c1/channel/%23a/events/3", a, 2, 1)
	e.want("/connection/c1/channel/%23a/events", b, 401, `{"status":"error","error":"Invalid connection id 'c1'"}`)
	e.want("/connection/c1/channel/%23a/events/abc", a, 400, `{"status":"error","error":"event id must be a number"}`)
}

func TestLastRoute(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	testenv.AddConnection(t, e.db, "c1", a, "{}")
	e.exec(`INSERT INTO log (connection, "user", channel, command, time, message) VALUES ('c1',$1,'#a','PRIVMSG',now(),'{"Params":["#a","running late again"]}')`, a)
	if c, b := e.getJSON("/connection/c1/channel/%23a/last?query=running%20late", a); c != 200 || len(msgIDs(t, b)) != 1 {
		t.Errorf("running late: %d %s", c, b)
	}
	e.want("/connection/c1/channel/%23a/last?query=what%3F", a, 200, `[]`)
	e.want("/connection/c1/channel/%23a/last", a, 200, `[]`)
}

func TestHistoryOtherMethodsNotFound(t *testing.T) {
	e := setup(t)
	for _, p := range []string{"/log/5", "/connection/c1/channel/a/events"} {
		checkNotFound(t, e, "POST", p, "")
	}
}
