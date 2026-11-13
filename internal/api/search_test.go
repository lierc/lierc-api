package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

type sse struct{ typ, data string }

// readSSE reads events from r until it ends, sending each on the channel.
func readSSE(r io.Reader, out chan<- sse) {
	defer close(out)
	br := bufio.NewReader(r)
	var ev sse
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			ev.typ = line[7:]
		case strings.HasPrefix(line, "data: "):
			ev.data = line[6:]
		case line == "" && ev.typ != "":
			out <- ev
			ev = sse{}
		}
	}
}

func (e *env) search(path, user string) []sse {
	e.t.Helper()
	res := e.do("GET", path, user)
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream;charset=utf-8" {
		e.t.Fatalf("GET %s: %d %q", path, res.StatusCode, res.Header.Get("Content-Type"))
	}
	ch := make(chan sse)
	go readSSE(res.Body, ch)
	var evs []sse
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

func searchSetup(t *testing.T) (*env, string) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	testenv.AddConnection(t, e.db, "c1", a, "{}")
	for i, text := range []string{"running late again", "something else", "more things"} {
		sender := "alice"
		if i == 1 {
			sender = "bob"
		}
		e.exec(`INSERT INTO log (id, connection, "user", channel, command, time, message) VALUES ($1,'c1',$2,'#a','PRIVMSG','2026-01-02 10:00',
			jsonb_build_object('Command','PRIVMSG','Params',jsonb_build_array('#a',$3::text),'Prefix',jsonb_build_object('Name',$4::text)))`, i+1, a, text, sender)
	}
	return e, a
}

const datePath = "/connection/c1/channel/%23a/date/2026-01-01/2026-01-03"

func logIDs(t *testing.T, evs []sse) []int64 {
	t.Helper()
	out := []int64{}
	for _, ev := range evs {
		if ev.typ != "log" {
			t.Errorf("unexpected %+v", ev)
			continue
		}
		var m struct {
			MessageId    int64
			Message      json.RawMessage
			ConnectionId string
			Highlight    *bool
		}
		if err := json.Unmarshal([]byte(ev.data), &m); err != nil || m.ConnectionId != "c1" || m.Highlight == nil || len(m.Message) == 0 {
			t.Errorf("event data %s: %v", ev.data, err)
		}
		out = append(out, m.MessageId)
	}
	return out
}

func TestDateSearch(t *testing.T) {
	e, a := searchSetup(t)
	for path, want := range map[string]string{
		datePath:                        "[1 2 3]",
		datePath + "?text=late%20again": "[1]",
		datePath + "?sender=bob":        "[2]",
		datePath + "?text=":             "[1 2 3]",
	} {
		if got := logIDs(t, e.search(path, a)); fmt.Sprint(got) != want {
			t.Errorf("%s: %v, want %s", path, got, want)
		}
	}
}

func TestDateSearchErrors(t *testing.T) {
	e, a := searchSetup(t)
	base := "/connection/c1/channel/%23a/date/"
	for path, want := range map[string]string{
		base + "2026-02-30/2026-03-01": `{"error":"Dates must be YYYY-MM-DD"}`,
		base + "20260101/2026-01-02":   `{"error":"Dates must be YYYY-MM-DD"}`,
		base + "2026-01-01/2026-02-01": `{"error":"Search at most 31 days at a time"}`,
	} {
		evs := e.search(path, a)
		if len(evs) != 1 || evs[0].typ != "search_error" || evs[0].data != want {
			t.Errorf("%s: %+v, want search_error %s", path, evs, want)
		}
	}
	for _, ev := range e.search(base+"2026-01-01/2026-01-31", a) {
		if ev.typ != "log" {
			t.Errorf("31 days inclusive: %+v", ev)
		}
	}
	if evs := e.search(base+"2026-01-03/2026-01-01", a); len(evs) != 0 {
		t.Errorf("backwards: %+v", evs)
	}
}

// swapDate replaces the date search's query for one test.
func swapDate(t *testing.T, f func(s db.Store, ctx context.Context, conn, channel, from, to string, text, sender *string, each func(db.Message) error) error) {
	old := channelDate
	channelDate = f
	t.Cleanup(func() { channelDate = old })
}

// Rows reach the browser as they're read, and a browser that leaves
// cancels the query. Postgres holds small rows in its send buffer, so the
// gap between rows is made in Go; the query left running is one long
// pg_sleep after the first row.
func TestDateSearchStreams(t *testing.T) {
	e, a := searchSetup(t)
	swapDate(t, func(_ db.Store, ctx context.Context, _, _, _, _ string, _, _ *string, each func(db.Message) error) error {
		if err := each(db.Message{MessageId: 1, Message: json.RawMessage(`{}`), ConnectionId: "c1"}); err != nil {
			return err
		}
		_, err := e.db.Pool.Exec(ctx, `SELECT pg_sleep(5) /* date-search-test */`)
		return err
	})
	start := time.Now()
	res := e.do("GET", datePath, a)
	ch := make(chan sse)
	go readSSE(res.Body, ch)
	select {
	case ev := <-ch:
		if ev.typ != "log" || time.Since(start) > time.Second {
			t.Errorf("first event %+v after %v", ev, time.Since(start))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no first event before the query finished")
	}
	res.Body.Close()
	active := func() int {
		var n int
		e.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE state = 'active' AND query LIKE '%date-search-test%' AND query NOT LIKE '%pg_stat_activity%'`).Scan(&n)
		return n
	}
	deadline := time.Now().Add(2 * time.Second)
	for active() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the query is still running after the browser left")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDateSearchFailsMidStream(t *testing.T) {
	e, a := searchSetup(t)
	swapDate(t, func(_ db.Store, ctx context.Context, _, _, _, _ string, _, _ *string, each func(db.Message) error) error {
		if err := each(db.Message{MessageId: 1, Message: json.RawMessage(`{}`), ConnectionId: "c1"}); err != nil {
			return err
		}
		return errors.New("boom")
	})
	evs := e.search(datePath, a)
	if len(evs) != 2 || evs[0].typ != "log" || evs[1].typ != "search_error" || evs[1].data != `{"error":"Search failed"}` {
		t.Errorf("events = %+v", evs)
	}
}

func TestDateSearchOwner(t *testing.T) {
	e, _ := searchSetup(t)
	b := testenv.AddUser(t, e.db, "bob")
	e.want(datePath, b, 401, `{"status":"error","error":"Invalid connection id 'c1'"}`)
}

// A client that stops reading (a frozen tab) mustn't hold the search, and
// its database connection, open: each write gets a deadline.
func TestDateSearchStalledClient(t *testing.T) {
	e, a := searchSetup(t)
	old := searchWriteTimeout
	searchWriteTimeout = 200 * time.Millisecond
	t.Cleanup(func() { searchWriteTimeout = old })
	big := json.RawMessage(`{"Params":["#a","` + strings.Repeat("x", 64<<10) + `"]}`)
	done := make(chan error, 1)
	swapDate(t, func(_ db.Store, ctx context.Context, _, _, _, _ string, _, _ *string, each func(db.Message) error) error {
		for i := int64(1); ; i++ {
			if err := each(db.Message{MessageId: i, Message: big, ConnectionId: "c1"}); err != nil {
				done <- err
				return err
			}
		}
	})
	conn, err := net.Dial("tcp", strings.TrimPrefix(e.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.(*net.TCPConn).SetReadBuffer(4096)
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: x\r\nCookie: chats=%s\r\n\r\n", datePath, testenv.Login(e.t, e.db, a))
	// never read
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the search is still writing to a client that stopped reading")
	}
}
