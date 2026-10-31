package stream

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard) // stream close / nsq logging
	testenv.Main(m)
}

const (
	testKey    = "statskey"
	testSecret = "testsecret"
	waitFor    = 10 * time.Second
)

type env struct {
	t      *testing.T
	db     *db.DB
	fake   *testenv.LiercdServer
	s      *Server
	srv    *httptest.Server
	replay chan struct{} // one value per finished replay
}

func noKeepAlive() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
}

// setup serves a Server talking to a fake liercd.
func setup(t *testing.T) *env {
	t.Helper()
	d := testenv.Postgres(t)
	fake := testenv.FakeLiercd(t)
	return setupWith(t, d, fake, fake.URL)
}

func setupWith(t *testing.T, d *db.DB, fake *testenv.LiercdServer, liercdURL string) *env {
	t.Helper()
	lc := &liercd.Client{BaseURL: liercdURL, HTTP: noKeepAlive()}
	s := New(config.Config{Key: testKey, Secret: testSecret}, d, lc)
	s.PingInterval = time.Hour
	s.WriteTimeout = 5 * time.Second
	e := &env{t: t, db: d, fake: fake, s: s, replay: make(chan struct{}, 100)}
	s.replayed = func() { e.replay <- struct{}{} }
	e.srv = httptest.NewServer(s)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) addUser(name string) string {
	e.t.Helper()
	return testenv.AddUser(e.t, e.db, name)
}

func (e *env) addConn(id, user, cfg string) {
	e.t.Helper()
	testenv.AddConnection(e.t, e.db, id, user, cfg)
}

// liveConn registers a connection whose liercd status is an unregistered,
// disconnected client (one CREATE event on replay).
func (e *env) liveConn(id, user string) {
	e.addConn(id, user, `{"Host":"irc.example"}`)
	e.fake.Respond("GET", "/"+id+"/status", 200,
		fmt.Sprintf(`{"Id":%q,"Nick":"n","Config":{"Host":"irc.example"},"Status":{"Connected":false},"Registered":false}`, id))
}

type event struct {
	typ, data, id string
	hasID         bool
}

type client struct {
	t      *testing.T
	id     string // from the stream's stream event
	resp   *http.Response
	events chan event
	errc   chan error
}

func (e *env) get(path, user string, hdr ...string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	if user != "" {
		req.AddCookie(&http.Cookie{Name: "chats", Value: testenv.PerlCookie(user, testSecret)})
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := noKeepAlive().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

// post sends an empty POST to path, logged in as user unless user is "".
func (e *env) post(path, user string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+path, nil)
	if user != "" {
		req.AddCookie(&http.Cookie{Name: "chats", Value: testenv.PerlCookie(user, testSecret)})
	}
	res, err := noKeepAlive().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	res.Body.Close()
	return res
}

// open starts a stream, checks the padding, and parses events in the
// background. Every event must be well-formed or the client records an error.
func (e *env) open(user string) *client {
	e.t.Helper()
	res := e.get("/", user, "User-Agent", "test-agent")
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		e.t.Fatalf("stream status %d: %s", res.StatusCode, b)
	}
	c := &client{t: e.t, resp: res, events: make(chan event, 10000), errc: make(chan error, 1)}
	e.t.Cleanup(c.close)
	br := bufio.NewReader(res.Body)
	pad := make([]byte, len(padding))
	if _, err := io.ReadFull(br, pad); err != nil || string(pad) != padding {
		e.t.Fatalf("padding = %q, %v", pad, err)
	}
	go c.parse(br)
	ev := c.next()
	var hello struct {
		Id string `json:"id"`
	}
	if ev.typ != "stream" || json.Unmarshal([]byte(ev.data), &hello) != nil || hello.Id == "" {
		e.t.Fatalf("first event %+v, want stream with an id", ev)
	}
	c.id = hello.Id
	return c
}

// lastID is the last_id of a ping event.
func lastID(t *testing.T, ev event) int64 {
	t.Helper()
	var p struct {
		LastID *int64 `json:"last_id"`
	}
	if ev.typ != "ping" || json.Unmarshal([]byte(ev.data), &p) != nil || p.LastID == nil {
		t.Fatalf("event %+v, want ping with last_id", ev)
	}
	return *p.LastID
}

func (c *client) close() { c.resp.Body.Close() }

var fieldRE = regexp.MustCompile(`^(event|data|id): (.*)$`)

func (c *client) parse(br *bufio.Reader) {
	defer close(c.events)
	line := func() (string, error) {
		l, err := br.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(l, "\n"), nil
	}
	fail := func(f string, a ...any) {
		select {
		case c.errc <- fmt.Errorf(f, a...):
		default:
		}
	}
	for {
		l, err := line()
		if err != nil {
			return
		}
		m := fieldRE.FindStringSubmatch(l)
		if m == nil || m[1] != "event" {
			fail("expected event line, got %q", l)
			return
		}
		ev := event{typ: m[2]}
		l, err = line()
		if err != nil {
			fail("truncated event %q", ev.typ)
			return
		}
		m = fieldRE.FindStringSubmatch(l)
		if m == nil || m[1] != "data" {
			fail("expected data line, got %q", l)
			return
		}
		ev.data = m[2]
		l, err = line()
		if err != nil {
			fail("truncated event %q", ev.typ)
			return
		}
		if m = fieldRE.FindStringSubmatch(l); m != nil && m[1] == "id" {
			ev.id, ev.hasID = m[2], true
			if l, err = line(); err != nil {
				fail("truncated event %q", ev.typ)
				return
			}
		}
		if l != "" {
			fail("expected blank line, got %q", l)
			return
		}
		c.events <- ev
	}
}

func (c *client) checkErr() {
	c.t.Helper()
	select {
	case err := <-c.errc:
		c.t.Fatal(err)
	default:
	}
}

func (c *client) next() event {
	c.t.Helper()
	select {
	case ev, ok := <-c.events:
		if !ok {
			c.checkErr()
			c.t.Fatal("stream closed")
		}
		return ev
	case <-time.After(waitFor):
		c.t.Fatal("timed out waiting for event")
	}
	return event{}
}

// nextIRC skips pings and NSQ readiness probes.
func (c *client) nextIRC() event {
	c.t.Helper()
	for {
		ev := c.next()
		if ev.typ == "irc" && !strings.Contains(ev.data, `"probe"`) {
			return ev
		}
	}
}

func (e *env) waitReplay() {
	e.t.Helper()
	select {
	case <-e.replay:
	case <-time.After(waitFor):
		e.t.Fatal("replay did not finish")
	}
}

// replayEvents waits for the stream's replay to finish, then pings all
// streams and returns the events before that ping.
func (e *env) replayEvents(c *client) []event {
	e.t.Helper()
	if ev := c.next(); ev.typ != "ping" {
		e.t.Fatalf("first event %q, want ping", ev.typ)
	}
	e.waitReplay()
	e.s.pingAll()
	var evs []event
	for {
		ev := c.next()
		if ev.typ == "ping" {
			return evs
		}
		evs = append(evs, ev)
	}
}

// openReplayed opens a stream and consumes its replay, so later events are live.
func (e *env) openReplayed(user string) *client {
	e.t.Helper()
	c := e.open(user)
	e.replayEvents(c)
	return c
}

func poll(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *Server) count(user string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streams[user])
}

func TestStatsKey(t *testing.T) {
	e := setup(t)
	a, b := e.addUser("alice"), e.addUser("bob")
	e.open(a)
	e.open(a)
	e.open(b)

	readJSON := func(res *http.Response) string {
		t.Helper()
		defer res.Body.Close()
		if ct := res.Header.Get("Content-Type"); ct != "application/javascript;charset=utf-8" {
			t.Errorf("Content-Type = %q", ct)
		}
		body, _ := io.ReadAll(res.Body)
		return string(body)
	}

	res := e.get("/stats", "", "lierc-key", testKey)
	if body := readJSON(res); res.StatusCode != 200 {
		t.Fatalf("stats %d %s", res.StatusCode, body)
	} else {
		var got map[string]int
		json.Unmarshal([]byte(body), &got)
		if len(got) != 2 || got[a] != 2 || got[b] != 1 {
			t.Errorf("stats = %s", body)
		}
	}

	res = e.get("/stats?user="+a, "", "lierc-key", testKey)
	body := readJSON(res)
	var details []map[string]any
	if err := json.Unmarshal([]byte(body), &details); err != nil || len(details) != 2 {
		t.Fatalf("user stats = %s", body)
	}
	for _, d := range details {
		created, ok := d["created"].(float64)
		if d["remote"] != "127.0.0.1" || d["agent"] != "test-agent" || !ok ||
			created != float64(int64(created)) || time.Since(time.Unix(int64(created), 0)) > time.Minute {
			t.Errorf("detail = %v", d)
		}
	}

	res = e.get("/stats?user=nobody", "", "lierc-key", testKey)
	if body := readJSON(res); body != "[]" {
		t.Errorf("unknown user stats = %s", body)
	}

	for _, hdr := range [][]string{{"lierc-key", "wrong"}, nil} {
		// Logged in or not, a bad key is rejected before auth.
		res := e.get("/stats", a, hdr...)
		body := readJSON(res)
		if res.StatusCode != 400 || body != `{"status":"error","error":"Invalid key"}` {
			t.Errorf("bad key %v: %d %s", hdr, res.StatusCode, body)
		}
	}
}

func TestUnauthorized(t *testing.T) {
	e := setup(t)
	for _, user := range []string{"", "ghost"} {
		res := e.get("/", user)
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 401 || string(body) != `{"status":"error","error":"unauthorized"}` ||
			res.Header.Get("Content-Type") != "application/javascript;charset=utf-8" {
			t.Errorf("user %q: %d %q %s", user, res.StatusCode, res.Header.Get("Content-Type"), body)
		}
	}
}

func TestHandshake(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	res := e.get("/", u)
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream;charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	br := bufio.NewReader(res.Body)
	pad := make([]byte, len(padding))
	if _, err := io.ReadFull(br, pad); err != nil || string(pad) != padding {
		t.Fatalf("padding = %q %v", pad, err)
	}
	readEvent := func() []byte {
		ev := make([]byte, 0, 64)
		for !strings.HasSuffix(string(ev), "\n\n") {
			b, err := br.ReadByte()
			if err != nil {
				t.Fatal(err)
			}
			ev = append(ev, b)
		}
		return ev
	}
	if ev := readEvent(); !regexp.MustCompile(`^event: stream\ndata: \{"id":"[^"]+"\}\n\n$`).Match(ev) {
		t.Errorf("stream event = %q", ev)
	}
	if ping := readEvent(); !regexp.MustCompile(`^event: ping\ndata: \{"time":\d{10}(\.\d+)?,"last_id":0\}\n\n$`).Match(ping) {
		t.Errorf("ping = %q", ping)
	}
}

func statusJSON(id, nick string, registered, connected bool, extra string) string {
	return fmt.Sprintf(`{"Id":%q,"Nick":%q,"Registered":%t,"Status":{"Connected":%t}%s}`,
		id, nick, registered, connected, extra)
}

func nicks(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("nick%03d", i)
	}
	return out
}

type irc struct {
	ConnectionId json.RawMessage
	Message      struct {
		Command string
		Prefix  struct{ Name json.RawMessage }
		Params  []json.RawMessage
		Time    json.RawMessage
	}
}

// line renders an irc event as "conn prefix COMMAND [params]" with raw JSON.
func decodeIRC(t *testing.T, ev event) (irc, string) {
	t.Helper()
	if ev.typ != "irc" || ev.hasID {
		t.Fatalf("unexpected event %+v", ev)
	}
	var m irc
	if err := json.Unmarshal([]byte(ev.data), &m); err != nil {
		t.Fatalf("decode %q: %v", ev.data, err)
	}
	var tm float64
	if err := json.Unmarshal(m.Message.Time, &tm); err != nil || time.Since(time.Unix(int64(tm), 0)) > time.Minute {
		t.Errorf("Time = %s", m.Message.Time)
	}
	var ps []string
	for _, p := range m.Message.Params {
		ps = append(ps, string(p))
	}
	return m, fmt.Sprintf("%s %s %s [%s]", m.ConnectionId, m.Message.Prefix.Name, m.Message.Command, strings.Join(ps, ","))
}

var stepOf = map[string]int{
	"CREATE": 0, "CONNECT": 0,
	"001": 1, "005": 1, "CAP": 1,
	"JOIN": 2,
	"332":  3, "333": 3,
	"353": 4, "366": 4,
	"324": 5,
}

func TestReplayOrder(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	n := nicks(120)
	nj, _ := json.Marshal(n)
	e.addConn("c1", u, `{"Host":"irc.one"}`)
	e.fake.Respond("GET", "/c1/status", 200, statusJSON("c1", "me1", true, true, `,
		"Config":{"Alias":"Net1","Host":"irc.one"},
		"Isupport":["A=1","B=2"],"CapsAcked":["sasl","multi-prefix"],
		"Channels":[
			{"Name":"#a","Mode":"+nt","Topic":{"Topic":"hello","User":"bob!b@h","Time":1700000000},"Nicks":`+string(nj)+`},
			{"Name":"#b","Mode":"","Topic":{"Topic":"","User":"","Time":0},"Nicks":["x","y","z"]}]`))
	e.addConn("c2", u, `{"Host":"irc.two"}`)
	e.fake.Respond("GET", "/c2/status", 200, statusJSON("c2", "me2", true, false, `,
		"Config":{"Alias":"","Host":"irc.two"},"Isupport":[],
		"Channels":[{"Name":"#c","Mode":"+s","Topic":{"Topic":"t2","User":"carol","Time":"1700000001"}}]`))
	e.addConn("c3", u, `{"Host":"irc.three"}`)
	e.fake.Respond("GET", "/c3/status", 200, statusJSON("c3", "me3", false, true, `,
		"Config":{"Host":"irc.three"},"Isupport":["X=1"],
		"Channels":[{"Name":"#d","Mode":"+n","Topic":{"Topic":"t3","User":"dan","Time":1},"Nicks":["q"]}]`))

	c := e.open(u)
	evs := e.replayEvents(c)

	perConn := map[string][]string{}
	last := 0
	for _, ev := range evs {
		m, line := decodeIRC(t, ev)
		step, ok := stepOf[m.Message.Command]
		if !ok || step < last {
			t.Errorf("event out of step order: %s", line)
		}
		last = step
		perConn[string(m.ConnectionId)] = append(perConn[string(m.ConnectionId)], line)
	}

	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	names := func(from, to int) string { return q(strings.Join(n[from:to], " ")) }
	want := map[string][]string{
		`"c1"`: {
			`"c1" "liercd" CREATE ["me1","Net1"]`,
			`"c1" "liercd" CONNECT []`,
			`"c1" "liercd" 001 ["me1","Welcome to the Internet Relay Network me1"]`,
			`"c1" "liercd" 005 ["me1","A=1 B=2"]`,
			`"c1" "liercd" CAP ["*","ACK","sasl multi-prefix"]`,
			`"c1" "me1" JOIN ["#a"]`,
			`"c1" "me1" JOIN ["#b"]`,
			`"c1" "liercd" 332 ["me1","#a","hello"]`,
			`"c1" "liercd" 333 ["me1","#a","bob!b@h",1700000000]`,
			`"c1" "liercd" 353 ["me1","=","#a",` + names(0, 50) + `]`,
			`"c1" "liercd" 353 ["me1","=","#a",` + names(50, 100) + `]`,
			`"c1" "liercd" 353 ["me1","=","#a",` + names(100, 120) + `]`,
			`"c1" "liercd" 366 ["me1","#a","End of /NAMES list."]`,
			`"c1" "liercd" 353 ["me1","=","#b","x y z"]`,
			`"c1" "liercd" 366 ["me1","#b","End of /NAMES list."]`,
			`"c1" "liercd" 324 ["#a","+nt"]`,
		},
		`"c2"`: {
			`"c2" "liercd" CREATE ["me2","irc.two"]`,
			`"c2" "liercd" 001 ["me2","Welcome to the Internet Relay Network me2"]`,
			`"c2" "me2" JOIN ["#c"]`,
			`"c2" "liercd" 332 ["me2","#c","t2"]`,
			`"c2" "liercd" 333 ["me2","#c","carol","1700000001"]`,
			`"c2" "liercd" 324 ["#c","+s"]`,
		},
		`"c3"`: {
			`"c3" "liercd" CREATE ["me3","irc.three"]`,
			`"c3" "liercd" CONNECT []`,
		},
	}
	for id, w := range want {
		got := perConn[id]
		if strings.Join(got, "\n") != strings.Join(w, "\n") {
			t.Errorf("%s events:\n%s\nwant:\n%s", id, strings.Join(got, "\n"), strings.Join(w, "\n"))
		}
	}
	if len(perConn) != len(want) {
		t.Errorf("connections in replay: %v", perConn)
	}
	c.checkErr()
}

func TestReplayRecreates(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	cfg := `{"Host":"irc.x","Nick":"n4","Channels":["#z"]}`
	e.addConn("c4", u, cfg)
	e.fake.SetStatus("c4", statusJSON("c4", "n4", false, false, `,"Config":{"Host":"irc.x"}`))

	c := e.open(u)
	evs := e.replayEvents(c)
	if len(evs) != 1 {
		t.Fatalf("events = %v", evs)
	}
	if _, line := decodeIRC(t, evs[0]); line != `"c4" "liercd" CREATE ["n4","irc.x"]` {
		t.Errorf("event = %s", line)
	}
	var created bool
	for _, call := range e.fake.Calls() {
		if call.Method == "POST" && call.Path == "/c4/create" {
			created = true
			if call.Body != cfg {
				t.Errorf("create body = %q, want %q", call.Body, cfg)
			}
		}
	}
	if !created {
		t.Errorf("no create call: %v", e.fake.Calls())
	}
}

// R4: one failing connection suppresses the whole replay; the stream stays open.
func TestReplayOneConnectionFails(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	e.liveConn("c1", u)
	e.addConn("c5", u, `{"Host":"irc.bad"}`)
	e.fake.Respond("GET", "/c5/status", 500, "boom")
	e.fake.Respond("POST", "/c5/create", 500, "boom")

	c := e.open(u)
	if evs := e.replayEvents(c); len(evs) != 0 {
		t.Fatalf("replay events = %v", evs)
	}
	e.s.PingInterval = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.s.Run(ctx)
	for i := 0; i < 3; i++ {
		if ev := c.next(); ev.typ != "ping" {
			t.Fatalf("event %+v, want ping", ev)
		}
	}
	if e.s.count(u) != 1 {
		t.Error("stream not registered")
	}
}

func consume(t *testing.T, s *Server, addr string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Consume(ctx, addr) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Consume: %v", err)
		}
	})
}

// awaitSubscribed publishes probe messages for conn until every client has
// seen one, so later messages reach every consumer's channel.
func awaitSubscribed(t *testing.T, publish func(string, []byte), conn string, cs ...*client) {
	t.Helper()
	probe := []byte(fmt.Sprintf(`{"ConnectionId":%q,"MessageId":0,"probe":true}`, conn))
	deadline := time.Now().Add(waitFor)
	for _, c := range cs {
		for seen := false; !seen; {
			if time.Now().After(deadline) {
				t.Fatal("consumers never subscribed")
			}
			publish("logged", probe)
			timeout := time.After(50 * time.Millisecond)
		drain:
			for {
				select {
				case ev := <-c.events:
					if strings.Contains(ev.data, `"probe"`) {
						seen = true
					}
				case <-timeout:
					break drain
				}
				if seen {
					break
				}
			}
		}
	}
}

func TestLiveDelivery(t *testing.T) {
	addr, publish := testenv.NSQD(t)
	e := setup(t)
	a, b := e.addUser("alice"), e.addUser("bob")
	e.liveConn("ca", a)
	e.liveConn("cb", b)
	a1, a2, b1 := e.openReplayed(a), e.openReplayed(a), e.openReplayed(b)
	consume(t, e.s, addr)
	awaitSubscribed(t, publish, "ca", a1, a2)
	awaitSubscribed(t, publish, "cb", b1)

	msgs := []struct{ body, id string }{
		{`{"ConnectionId":"ca", "MessageId":42,"Message":{"Command":"PRIVMSG","Params":["#a","hi  there"]}}`, "42"},
		{`{"ConnectionId":"ca","MessageId":0,"Message":{}}`, ""},
		{`{"ConnectionId":"ca","Message":{}}`, ""},
		{`{"ConnectionId":"ca","MessageId":"0"}`, ""},
		{`{"ConnectionId":"ca","MessageId":"abc"}`, "abc"},
	}
	for _, m := range msgs {
		publish("logged", []byte(m.body))
	}
	publish("logged", []byte(`{"ConnectionId":"cb","MessageId":7}`))
	publish("logged", []byte(`{"ConnectionId":"nobody","MessageId":8}`))

	for _, c := range []*client{a1, a2} {
		for _, m := range msgs {
			ev := c.nextIRC()
			if ev.data != m.body || ev.hasID != (m.id != "") || ev.id != m.id {
				t.Errorf("got %+v, want data %s id %q", ev, m.body, m.id)
			}
		}
	}
	// bob's first live event is his own message: none of alice's reached him.
	if ev := b1.nextIRC(); ev.data != `{"ConnectionId":"cb","MessageId":7}` || ev.id != "7" {
		t.Errorf("bob got %+v", ev)
	}
}

func TestPingLastID(t *testing.T) {
	addr, publish := testenv.NSQD(t)
	e := setup(t)
	a, b := e.addUser("alice"), e.addUser("bob")
	e.liveConn("ca", a)
	e.liveConn("cb", b)
	testenv.AddLog(t, e.db, 5, "ca", a)
	testenv.AddLog(t, e.db, 9, "ca", a)
	testenv.AddLog(t, e.db, 50, "cb", b)

	ac, bc := e.open(a), e.open(b)
	if id := lastID(t, ac.next()); id != 9 {
		t.Errorf("alice handshake last_id = %d, want 9", id)
	}
	if id := lastID(t, bc.next()); id != 50 {
		t.Errorf("bob handshake last_id = %d, want 50", id)
	}
	e.waitReplay()
	e.waitReplay()
	consume(t, e.s, addr)
	awaitSubscribed(t, publish, "ca", ac)

	// nextPing skips replay and probe events.
	nextPing := func(c *client) int64 {
		t.Helper()
		for {
			if ev := c.next(); ev.typ == "ping" {
				return lastID(t, ev)
			}
		}
	}
	publish("logged", []byte(`{"ConnectionId":"ca","MessageId":20}`))
	if ev := ac.nextIRC(); ev.id != "20" {
		t.Fatalf("got %+v, want message 20", ev)
	}
	e.s.pingAll()
	if id := nextPing(ac); id != 20 {
		t.Errorf("alice last_id after 20 = %d", id)
	}
	if id := nextPing(bc); id != 50 {
		t.Errorf("bob last_id = %d, want 50", id)
	}

	publish("logged", []byte(`{"ConnectionId":"ca","MessageId":15}`))
	if ev := ac.nextIRC(); ev.id != "15" {
		t.Fatalf("got %+v, want message 15", ev)
	}
	e.s.pingAll()
	if id := nextPing(ac); id != 20 {
		t.Errorf("alice last_id after 15 = %d, want 20", id)
	}
	if id := nextPing(bc); id != 50 {
		t.Errorf("bob last_id = %d, want 50", id)
	}
}

// A user's second stream reuses the newest id Deliver keeps current, rather
// than querying it again.
func TestSeedSkippedWhileStreamOpen(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	testenv.AddLog(t, e.db, 9, "ca", u)
	c1 := e.open(u)
	if id := lastID(t, c1.next()); id != 9 {
		t.Fatalf("first stream last_id = %d, want 9", id)
	}
	testenv.AddLog(t, e.db, 30, "ca", u) // logged, but never delivered
	c2 := e.open(u)
	if id := lastID(t, c2.next()); id != 9 {
		t.Errorf("second stream last_id = %d, want 9 (not queried again)", id)
	}
	c1.close()
	c2.close()
	poll(t, "streams to close", func() bool { return e.s.count(u) == 0 })
	if id := lastID(t, e.open(u).next()); id != 30 {
		t.Errorf("stream after the last closed: last_id = %d, want 30 (queried again)", id)
	}
}

func TestLatestDroppedWithLastStream(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	testenv.AddLog(t, e.db, 3, "ca", u)
	c1, c2 := e.open(u), e.open(u)
	latest := func() (int64, bool) {
		e.s.mu.Lock()
		defer e.s.mu.Unlock()
		id, ok := e.s.latest[u]
		return id, ok
	}
	if id, ok := latest(); !ok || id != 3 {
		t.Fatalf("latest = %d %v, want 3", id, ok)
	}
	c1.close()
	poll(t, "first stream to close", func() bool { return e.s.count(u) == 1 })
	if _, ok := latest(); !ok {
		t.Fatal("latest dropped while a stream is open")
	}
	c2.close()
	poll(t, "latest to be dropped", func() bool { _, ok := latest(); return !ok })
}

func TestWakePing(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	testenv.AddLog(t, e.db, 7, "ca", u)
	c1, c2 := e.open(u), e.open(u)
	lastID(t, c1.next())
	lastID(t, c2.next())
	e.waitReplay()
	e.waitReplay()

	if res := e.post("/"+c1.id+"/ping", u); res.StatusCode != 204 {
		t.Fatalf("ping status %d, want 204", res.StatusCode)
	}
	if id := lastID(t, c1.next()); id != 7 {
		t.Errorf("wake ping last_id = %d, want 7", id)
	}
	// Only c1 was pinged: c2's next ping is pingAll's, with nothing after it.
	e.s.pingAll()
	lastID(t, c1.next())
	lastID(t, c2.next())
	select {
	case ev := <-c2.events:
		t.Errorf("c2 got an extra %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWakePingDoubleSlash(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	c := e.open(u)
	if res := e.post("//"+c.id+"/ping", u); res.StatusCode != 204 {
		t.Fatalf("ping status %d, want 204", res.StatusCode)
	}
}

func TestWakePingUnknown(t *testing.T) {
	e := setup(t)
	a, b := e.addUser("alice"), e.addUser("bob")
	c := e.open(a)

	req, _ := http.NewRequest("POST", e.srv.URL+"/nope/ping", nil)
	req.AddCookie(&http.Cookie{Name: "chats", Value: testenv.PerlCookie(a, testSecret)})
	res, err := noKeepAlive().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 404 || string(body) != `{"status":"error","error":"unknown stream"}` {
		t.Errorf("unknown stream: %d %s", res.StatusCode, body)
	}
	if res := e.post("/"+c.id+"/ping", b); res.StatusCode != 404 {
		t.Errorf("bob pinging alice's stream: %d, want 404", res.StatusCode)
	}
	c.close()
	poll(t, "stream to close", func() bool { return e.s.count(a) == 0 })
	if res := e.post("/"+c.id+"/ping", a); res.StatusCode != 404 {
		t.Errorf("closed stream: %d, want 404", res.StatusCode)
	}
}

func TestWakePingUnauthorized(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	c := e.open(u)
	if res := e.post("/"+c.id+"/ping", ""); res.StatusCode != 401 {
		t.Errorf("no cookie: %d, want 401", res.StatusCode)
	}
}

func TestStreamStillServedOnOtherPaths(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	res := e.get("/x/ping", u)
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream;charset=utf-8" {
		t.Errorf("GET /x/ping: %d %s, want a stream", res.StatusCode, res.Header.Get("Content-Type"))
	}
}

// freeAddr returns a local TCP address that nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestNSQReconnectDefault(t *testing.T) {
	if got := New(config.Config{}, nil, nil).NSQReconnect; got != 5*time.Second {
		t.Fatalf("NSQReconnect = %s, want 5s", got)
	}
}

// nsqd is not running when Consume starts; it subscribes once nsqd is up.
func TestConsumeWaitsForNSQD(t *testing.T) {
	e := setup(t)
	e.s.NSQReconnect = 50 * time.Millisecond
	u := e.addUser("alice")
	e.liveConn("ca", u)
	c := e.openReplayed(u)
	addr := freeAddr(t)
	consume(t, e.s, addr)
	time.Sleep(200 * time.Millisecond) // a few failed attempts

	_, publish, _ := testenv.NSQDAt(t, addr)
	awaitSubscribed(t, publish, "ca", c)
	publish("logged", []byte(`{"ConnectionId":"ca","MessageId":1}`))
	if ev := c.nextIRC(); ev.id != "1" {
		t.Fatalf("got %+v", ev)
	}
}

// nsqd restarts; the consumer reconnects after NSQReconnect.
func TestConsumeReconnects(t *testing.T) {
	addr, publish, stop := testenv.NSQDAt(t, "127.0.0.1:0")
	e := setup(t)
	e.s.NSQReconnect = 50 * time.Millisecond
	u := e.addUser("alice")
	e.liveConn("ca", u)
	c := e.openReplayed(u)
	consume(t, e.s, addr)
	awaitSubscribed(t, publish, "ca", c)

	stop()
	_, publish, _ = testenv.NSQDAt(t, addr)
	awaitSubscribed(t, publish, "ca", c)
	publish("logged", []byte(`{"ConnectionId":"ca","MessageId":2}`))
	if ev := c.nextIRC(); ev.id != "2" {
		t.Fatalf("got %+v", ev)
	}
}

// Consume returns nil when ctx ends while nsqd has never been reachable.
func TestConsumeStopsWhileWaiting(t *testing.T) {
	e := setup(t)
	e.s.NSQReconnect = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.s.Consume(ctx, freeAddr(t)) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Consume: %v", err)
		}
	case <-time.After(waitFor):
		t.Fatal("Consume did not return")
	}
}

func TestTwoConsumersBothDeliver(t *testing.T) {
	addr, publish := testenv.NSQD(t)
	e1 := setup(t)
	e2 := setupWith(t, e1.db, e1.fake, e1.fake.URL)
	u := e1.addUser("alice")
	e1.liveConn("ca", u)
	c1, c2 := e1.openReplayed(u), e2.openReplayed(u)
	consume(t, e1.s, addr)
	consume(t, e2.s, addr)
	awaitSubscribed(t, publish, "ca", c1, c2)

	for i := 1; i <= 20; i++ {
		publish("logged", []byte(fmt.Sprintf(`{"ConnectionId":"ca","MessageId":%d}`, i)))
	}
	for _, c := range []*client{c1, c2} {
		for i := 1; i <= 20; i++ {
			if ev := c.nextIRC(); ev.id != fmt.Sprint(i) {
				t.Fatalf("got %+v, want id %d", ev, i)
			}
		}
	}
}

func TestConcurrentWritesRace(t *testing.T) {
	addr, publish := testenv.NSQD(t)
	e := setup(t)
	u := e.addUser("alice")
	e.liveConn("ca", u)
	c := e.openReplayed(u)
	consume(t, e.s, addr)
	awaitSubscribed(t, publish, "ca", c)

	e.s.PingInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.s.Run(ctx)

	const viaNSQ, workers, perWorker = 300, 4, 100
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				e.s.Deliver([]byte(fmt.Sprintf(`{"ConnectionId":"ca","MessageId":"d%d-%d"}`, w, i)))
			}
		}(w)
	}
	for i := 0; i < viaNSQ; i++ {
		publish("logged", []byte(fmt.Sprintf(`{"ConnectionId":"ca","MessageId":"n%d"}`, i)))
	}
	wg.Wait()

	seen := map[string]bool{}
	pings := 0
	for len(seen) < viaNSQ+workers*perWorker {
		ev := c.next()
		switch {
		case ev.typ == "ping":
			pings++
		case strings.Contains(ev.data, `"probe"`):
		default:
			var m struct{ MessageId string }
			if err := json.Unmarshal([]byte(ev.data), &m); err != nil || m.MessageId != ev.id || seen[ev.id] {
				t.Fatalf("bad event %+v", ev)
			}
			seen[ev.id] = true
		}
	}
	c.checkErr()
	if pings == 0 {
		t.Error("no pings interleaved")
	}
}

// gate forwards to the fake liercd, except that the first GET /c1/status
// blocks until the caller gives up.
func gate(t *testing.T, fake *testenv.LiercdServer, entered chan<- struct{}) string {
	var once sync.Once
	hc := noKeepAlive()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blocked := false
		if r.Method == "GET" && r.URL.Path == "/c1/status" {
			once.Do(func() { blocked = true })
		}
		if blocked {
			entered <- struct{}{}
			<-r.Context().Done()
			return
		}
		req, _ := http.NewRequest(r.Method, fake.URL+r.URL.Path, r.Body)
		res, err := hc.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer res.Body.Close()
		w.WriteHeader(res.StatusCode)
		io.Copy(w, res.Body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestCloseSavesChannels(t *testing.T) {
	d := testenv.Postgres(t)
	fake := testenv.FakeLiercd(t)
	entered := make(chan struct{}, 1)
	e := setupWith(t, d, fake, gate(t, fake, entered))
	u := e.addUser("alice")
	e.addConn("c1", u, `{"Host":"irc.x","Channels":["#old"],"Nick":"me"}`)
	fake.Respond("GET", "/c1/status", 200, statusJSON("c1", "me", true, true,
		`,"Isupport":[],"Channels":[{"Name":"#a"},{"Name":"#b"}]`))
	ctx := context.Background()

	runtime.GC()
	baseline := runtime.NumGoroutine()

	c := e.open(u)
	select {
	case <-entered:
	case <-time.After(waitFor):
		t.Fatal("replay never asked liercd for status")
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE "user" SET last_login='2000-01-01' WHERE id=$1`, u); err != nil {
		t.Fatal(err)
	}
	c.close() // disconnect mid-replay

	e.waitReplay()
	poll(t, "last_login", func() bool {
		var old bool
		d.Pool.QueryRow(ctx, `SELECT last_login < '2001-01-01' FROM "user" WHERE id=$1`, u).Scan(&old)
		return !old
	})
	var cfg map[string]any
	var raw []byte
	if err := d.Pool.QueryRow(ctx, `SELECT config FROM connection WHERE id='c1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &cfg)
	if fmt.Sprint(cfg["Channels"]) != "[#a #b]" || cfg["Host"] != "irc.x" || cfg["Nick"] != "me" {
		t.Errorf("config = %s", raw)
	}
	if n := e.s.count(u); n != 0 {
		t.Errorf("%d streams still registered", n)
	}
	poll(t, "goroutines to return to baseline", func() bool {
		return runtime.NumGoroutine() <= baseline
	})
}

func TestWriteTimeout(t *testing.T) {
	e := setup(t)
	e.s.WriteTimeout = 200 * time.Millisecond
	u := e.addUser("alice")
	e.liveConn("ca", u)

	conn, err := net.Dial("tcp", e.srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.(*net.TCPConn).SetReadBuffer(4096)
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nCookie: chats=%s\r\n\r\n", testenv.PerlCookie(u, testSecret))
	// Never read from conn again.

	poll(t, "stream registration", func() bool { return e.s.count(u) == 1 })
	e.waitReplay()
	e.s.mu.Lock()
	var st *stream
	for _, v := range e.s.streams[u] {
		st = v
	}
	e.s.mu.Unlock()

	big := []byte(`{"ConnectionId":"ca","pad":"` + strings.Repeat("x", 256<<10) + `"}`)
	// Deliver one message at a time (never filling the buffer) until the
	// writer stalls on the full socket.
	for i := 0; ; i++ {
		if i == 400 {
			t.Fatal("writer never stalled")
		}
		e.s.Deliver(big)
		drained := false
		for end := time.Now().Add(100 * time.Millisecond); time.Now().Before(end); time.Sleep(time.Millisecond) {
			if len(st.out) == 0 {
				drained = true
				break
			}
		}
		if !drained || e.s.count(u) == 0 {
			break
		}
	}
	poll(t, "slow client to be dropped", func() bool { return e.s.count(u) == 0 })
}

func TestSlowClientDropped(t *testing.T) {
	e := setup(t)
	e.s.WriteTimeout = time.Hour // only the buffer rule can drop it
	u := e.addUser("alice")
	e.liveConn("ca", u)

	conn, err := net.Dial("tcp", e.srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() // unblocks the stalled writer so the handler can finish
	conn.(*net.TCPConn).SetReadBuffer(4096)
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nCookie: chats=%s\r\n\r\n", testenv.PerlCookie(u, testSecret))
	// Never read from conn again.

	poll(t, "stream registration", func() bool { return e.s.count(u) == 1 })
	e.waitReplay()
	e.s.mu.Lock()
	var st *stream
	for _, v := range e.s.streams[u] {
		st = v
	}
	e.s.mu.Unlock()

	msg := []byte(`{"ConnectionId":"ca","pad":"` + strings.Repeat("x", 32<<10) + `"}`)
	for i := 0; e.s.count(u) == 1; i++ {
		if i == 20000 {
			t.Fatal("stream never dropped")
		}
		e.s.Deliver(msg)
	}
	// Deregistered by the delivery that overflowed, not later by the handler.
	select {
	case <-st.done:
	default:
		t.Error("stream deregistered but not stopped")
	}
	if len(st.out) != bufferSize {
		t.Errorf("dropped with %d queued, want a full buffer (%d)", len(st.out), bufferSize)
	}
	res := e.get("/stats", "", "lierc-key", testKey)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "{}" {
		t.Errorf("stats after drop = %s", body)
	}
}

// A replay far larger than the live buffer, to a client that reads slowly,
// must not crowd out pings and live events and get the stream dropped.
func TestLargeReplaySurvivesLiveEvents(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	const channels = 3000
	chans := make([]string, channels)
	long := strings.Repeat("c", 300)
	for i := range chans {
		chans[i] = fmt.Sprintf(`{"Name":"#%s%04d"}`, long, i)
	}
	e.addConn("ca", u, `{"Host":"irc.example"}`)
	e.fake.Respond("GET", "/ca/status", 200, statusJSON("ca", "me", true, true,
		`,"Isupport":[],"Channels":[`+strings.Join(chans, ",")+`]`))

	// A small receive buffer, and no reading until the live events are queued,
	// so the replay backs up behind the socket.
	dialer := &net.Dialer{Control: func(_, _ string, rc syscall.RawConn) error {
		return rc.Control(func(fd uintptr) {
			syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4096)
		})
	}}
	hc := &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: dialer.DialContext}}
	req, _ := http.NewRequest("GET", e.srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: "chats", Value: testenv.PerlCookie(u, testSecret)})
	res, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, resp: res, events: make(chan event, 20000), errc: make(chan error, 1)}
	t.Cleanup(c.close)

	poll(t, "replay to fetch status", func() bool {
		for _, call := range e.fake.Calls() {
			if call.Path == "/ca/status" {
				return true
			}
		}
		return false
	})
	// Paced so the live events arrive while the replay is backed up behind the
	// unread socket (the replay itself takes no time to build).
	const live = 300
	for i := 0; i < live; i++ {
		time.Sleep(time.Millisecond)
		e.s.Deliver([]byte(fmt.Sprintf(`{"ConnectionId":"ca","MessageId":"L%d"}`, i)))
		if i%10 == 0 {
			e.s.pingAll()
		}
	}
	if e.s.count(u) != 1 {
		t.Fatal("stream dropped while replay was backed up")
	}

	br := bufio.NewReader(res.Body)
	pad := make([]byte, len(padding))
	if _, err := io.ReadFull(br, pad); err != nil || string(pad) != padding {
		t.Fatalf("padding = %q, %v", pad, err)
	}
	go c.parse(br)

	joins, seen := 0, map[string]bool{}
	for joins < channels || len(seen) < live {
		ev := c.next()
		switch {
		case ev.typ == "ping":
		case ev.hasID:
			seen[ev.id] = true
		case strings.Contains(ev.data, `"Command":"JOIN"`):
			joins++
		}
	}
	c.checkErr()
	if e.s.count(u) != 1 {
		t.Error("stream not registered after replay")
	}
}

// drained waits for the server to end c's stream, skipping any events.
func (c *client) drained() {
	c.t.Helper()
	for {
		select {
		case _, ok := <-c.events:
			if !ok {
				c.checkErr()
				return
			}
		case <-time.After(waitFor):
			c.t.Fatal("stream still open")
		}
	}
}

// Shutdown ends open streams at once instead of waiting out its deadline,
// and streams opened while it runs end as soon as they start.
func TestShutdownClosesStreams(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	e.liveConn("c1", u)
	c := e.openReplayed(u)
	e.srv.Config.RegisterOnShutdown(e.s.CloseStreams)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	start := time.Now()
	if err := e.srv.Config.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v after %v", err, time.Since(start))
	}
	c.drained()
	if n := e.s.count(u); n != 0 {
		t.Errorf("%d streams still registered", n)
	}
}

func TestStreamAfterCloseStreamsEnds(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	e.liveConn("c1", u)
	e.s.CloseStreams()
	c := e.open(u)
	c.drained()
	if n := e.s.count(u); n != 0 {
		t.Errorf("%d streams still registered", n)
	}
}
