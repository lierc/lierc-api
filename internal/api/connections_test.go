package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/testenv"
)

var connRoutes = [][2]string{
	{"GET", "/connection"}, {"POST", "/connection"}, {"GET", "/connection/c1"}, {"PUT", "/connection/c1"},
	{"PUT", "/connection/c1/config"}, {"DELETE", "/connection/c1"}, {"POST", "/connection/c1"},
}

func TestConnectionsNeedLogin(t *testing.T) {
	e := setup(t)
	for _, rq := range connRoutes {
		e.sendWant(rq[0], rq[1], "", "{}", 401, `{"status":"error","error":"unauthorized"}`)
	}
}

func TestListAndShow(t *testing.T) {
	e := setup(t)
	a, b := testenv.AddUser(t, e.db, "alice"), testenv.AddUser(t, e.db, "bob")
	testenv.AddConnection(t, e.db, "c1", a, `{"Host":"irc.one","Nick":"me"}`)
	testenv.AddConnection(t, e.db, "c2", a, `{"Host":"irc.two"}`)
	c, body := e.getJSON("/connection", a)
	var list []struct {
		Id     string
		Config map[string]any
	}
	if err := json.Unmarshal([]byte(body), &list); c != 200 || err != nil || len(list) != 2 {
		t.Fatalf("list: %d %s %v", c, body, err)
	}
	for _, l := range list {
		if (l.Id == "c1" && l.Config["Host"] != "irc.one") || (l.Id == "c2" && l.Config["Host"] != "irc.two") {
			t.Errorf("entry %+v", l)
		}
	}
	e.want("/connection", b, 200, `[]`)
	c, body = e.getJSON("/connection/c1", a)
	var show struct {
		Id     string
		Config map[string]any
	}
	if json.Unmarshal([]byte(body), &show); c != 200 || show.Id != "c1" || show.Config["Nick"] != "me" {
		t.Errorf("show: %d %s", c, body)
	}
	e.want("/connection/c1", b, 401, `{"status":"error","error":"Invalid connection id 'c1'"}`)
}

func TestCreateRoute(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	c, body := e.send("POST", "/connection", a, `{"Host":"irc.example","Port":6667,"Nick":"me"}`)
	var res struct{ Success, Id string }
	if json.Unmarshal([]byte(body), &res); c != 200 || res.Success != "ok" || len(res.Id) < 20 || !strings.HasPrefix(body, `{"success":"ok","id":"`) {
		t.Errorf("create: %d %s", c, body)
	}
	e.sendWant("POST", "/connection", a, `[]`, 400, `{"status":"error","error":"Invalid connection settings"}`)
	for i := 0; i < 4; i++ {
		e.send("POST", "/connection", a, `{"Host":"irc.example","Port":6667,"Nick":"me"}`)
	}
	e.sendWant("POST", "/connection", a, `{"Host":"irc.example","Port":6667,"Nick":"me"}`, 400, `{"status":"error","error":"You can have at most 5 connections"}`)
}

func TestEditSaveDeleteRoutes(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	testenv.AddConnection(t, e.db, "c1", a, `{"Host":"irc.example","Port":6667,"Nick":"me","Channels":[]}`)
	testenv.AddConnection(t, e.db, "c2", a, `{}`)
	e.sendWant("PUT", "/connection/c1", a, `{"Host":"irc.example","Port":6667,"Nick":"me","Alias":"Net"}`, 200, `{"status":"ok"}`)
	e.sendWant("PUT", "/connection/c1/config", a, `{"Alias":"Other"}`, 200, `{"status":"ok"}`)
	if v := e.row(`SELECT config::json->>'Alias' FROM connection WHERE id='c1'`); v != "Other" {
		t.Errorf("alias = %s", v)
	}
	e.fake.Respond("POST", "/c1/destroy", 200, "ok")
	e.sendWant("DELETE", "/connection/c1", a, "", 200, `{"status":"ok"}`)
	e.fake.Respond("POST", "/c2/destroy", 404, "nok\n")
	e.sendWant("DELETE", "/connection/c2", a, "", 200, `{"status":"ok"}`)
	if v := e.row(`SELECT count(*)::text FROM connection`); v != "0" {
		t.Errorf("%s rows left", v)
	}
}

func TestSendRoute(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	testenv.AddConnection(t, e.db, "c1", a, "{}")
	e.fake.Respond("POST", "/c1/raw", 200, "ok")
	if c, b := e.send("POST", "/connection/c1", a, "PRIVMSG #a :hi", "Content-Type", "application/irc"); c != 200 || b != `{"status":"ok"}` {
		t.Errorf("send: %d %s", c, b)
	}
	calls := e.fake.Calls()
	if last := calls[len(calls)-1]; last.Path != "/c1/raw" || last.Body != "PRIVMSG #a :hi" {
		t.Errorf("liercd got %+v", last)
	}
	e.fake.Respond("POST", "/c1/raw", 409, "not registered")
	e.sendWant("POST", "/connection/c1", a, "PRIVMSG #a :hi", 400, `{"status":"error","error":"not registered"}`)
}

func TestConnectionsCrossSite(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	if c, _ := e.send("POST", "/connection", a, `{"Host":"irc.example","Port":6667,"Nick":"me"}`, "Sec-Fetch-Site", "cross-site"); c != 403 {
		t.Errorf("cross-site: %d", c)
	}
	if v := e.row(`SELECT count(*)::text FROM connection`); v != "0" {
		t.Errorf("%s rows created", v)
	}
}
