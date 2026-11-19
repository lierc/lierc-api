package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

// dateResult is a date search's response body.
type dateResult struct {
	Messages []struct {
		MessageId    int64
		Message      json.RawMessage
		ConnectionId string
		Highlight    *bool
	} `json:"messages"`
	Truncated *bool `json:"truncated"`
}

// search GETs a date search that should succeed, returning its body.
func (e *env) search(path, user string) dateResult {
	e.t.Helper()
	c, b := e.getJSON(path, user)
	if c != 200 {
		e.t.Fatalf("GET %s: %d %s", path, c, b)
	}
	var r dateResult
	if err := json.Unmarshal([]byte(b), &r); err != nil || r.Messages == nil || r.Truncated == nil {
		e.t.Fatalf("GET %s: body %s: %v", path, b, err)
	}
	return r
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
		e.exec(`INSERT INTO logs (id, connection_id, user_id, channel, command, time, message) VALUES ($1,'c1',$2,'#a','PRIVMSG','2026-01-02 10:00',
			jsonb_build_object('Command','PRIVMSG','Params',jsonb_build_array('#a',$3::text),'Prefix',jsonb_build_object('Name',$4::text)))`, i+1, a, text, sender)
	}
	return e, a
}

const datePath = "/connection/c1/channel/%23a/date/2026-01-01/2026-01-03"

func resultIDs(t *testing.T, r dateResult) []int64 {
	t.Helper()
	out := []int64{}
	for _, m := range r.Messages {
		if m.ConnectionId != "c1" || m.Highlight == nil || len(m.Message) == 0 {
			t.Errorf("message %+v", m)
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
		r := e.search(path, a)
		if got := resultIDs(t, r); fmt.Sprint(got) != want || *r.Truncated {
			t.Errorf("%s: %v (truncated %v), want %s", path, got, *r.Truncated, want)
		}
	}
}

func TestDateSearchErrors(t *testing.T) {
	e, a := searchSetup(t)
	base := "/connection/c1/channel/%23a/date/"
	for path, want := range map[string]string{
		base + "2026-02-30/2026-03-01": `{"status":"error","error":"Dates must be YYYY-MM-DD"}`,
		base + "20260101/2026-01-02":   `{"status":"error","error":"Dates must be YYYY-MM-DD"}`,
		base + "2026-01-01/2026-02-01": `{"status":"error","error":"Search at most 31 days at a time"}`,
	} {
		e.want(path, a, 400, want)
	}
	if r := e.search(base+"2026-01-01/2026-01-31", a); len(r.Messages) != 3 {
		t.Errorf("31 days inclusive: %v", resultIDs(t, r))
	}
	if r := e.search(base+"2026-01-03/2026-01-01", a); len(r.Messages) != 0 {
		t.Errorf("backwards: %v", resultIDs(t, r))
	}
}

// Past the limit, the oldest dateLimit messages are sent, marked truncated.
func TestDateSearchTruncated(t *testing.T) {
	e, a := searchSetup(t)
	old := dateLimit
	dateLimit = 2
	t.Cleanup(func() { dateLimit = old })
	r := e.search(datePath, a)
	if got := resultIDs(t, r); fmt.Sprint(got) != "[1 2]" || !*r.Truncated {
		t.Errorf("%v (truncated %v), want [1 2], truncated", got, *r.Truncated)
	}
	dateLimit = 3
	if r := e.search(datePath, a); len(r.Messages) != 3 || *r.Truncated {
		t.Errorf("exactly the limit: %v (truncated %v)", resultIDs(t, r), *r.Truncated)
	}
}

// swapDate replaces the date search's query for one test.
func swapDate(t *testing.T, f func(s db.Store, ctx context.Context, conn, channel, from, to string, text, sender *string, limit int) ([]db.Message, error)) {
	old := channelDate
	channelDate = f
	t.Cleanup(func() { channelDate = old })
}

func TestDateSearchFails(t *testing.T) {
	e, a := searchSetup(t)
	swapDate(t, func(db.Store, context.Context, string, string, string, string, *string, *string, int) ([]db.Message, error) {
		return nil, errors.New("boom")
	})
	e.want(datePath, a, 500, `{"status":"error","error":"internal error"}`)
}

// A browser that leaves cancels the query.
func TestDateSearchCancels(t *testing.T) {
	e, a := searchSetup(t)
	swapDate(t, func(_ db.Store, ctx context.Context, _, _, _, _ string, _, _ *string, _ int) ([]db.Message, error) {
		_, err := e.db.Pool.Exec(ctx, `SELECT pg_sleep(5) /* date-search-test */`)
		return nil, err
	})
	cookie := testenv.Login(t, e.db, a)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequestWithContext(ctx, "GET", e.srv.URL+datePath, nil)
		req.AddCookie(&http.Cookie{Name: "chats", Value: cookie})
		if res, err := e.srv.Client().Do(req); err == nil {
			res.Body.Close()
		}
	}()
	active := func() int {
		var n int
		e.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE state = 'active' AND query LIKE '%date-search-test%' AND query NOT LIKE '%pg_stat_activity%'`).Scan(&n)
		return n
	}
	deadline := time.Now().Add(3 * time.Second)
	for active() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the query never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	deadline = time.Now().Add(2 * time.Second)
	for active() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the query is still running after the browser left")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDateSearchOwner(t *testing.T) {
	e, _ := searchSetup(t)
	b := testenv.AddUser(t, e.db, "bob")
	e.want(datePath, b, 401, `{"status":"error","error":"Invalid connection id 'c1'"}`)
}

// A client that stops reading (a frozen tab) mustn't hold the response
// open: the write has a deadline.
func TestDateSearchStalledClient(t *testing.T) {
	e, a := searchSetup(t)
	old := dateWriteTimeout
	dateWriteTimeout = 200 * time.Millisecond
	t.Cleanup(func() { dateWriteTimeout = old })
	big := json.RawMessage(`{"Params":["#a","` + strings.Repeat("x", 64<<10) + `"]}`)
	written := make(chan struct{})
	swapDate(t, func(db.Store, context.Context, string, string, string, string, *string, *string, int) ([]db.Message, error) {
		ms := make([]db.Message, 200) // ~13 MB
		for i := range ms {
			ms[i] = db.Message{MessageId: int64(i + 1), Message: big, ConnectionId: "c1"}
		}
		return ms, nil
	})
	dateWritten = func() { close(written) }
	t.Cleanup(func() { dateWritten = func() {} })
	conn, err := net.Dial("tcp", strings.TrimPrefix(e.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.(*net.TCPConn).SetReadBuffer(4096)
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: x\r\nCookie: chats=%s\r\n\r\n", datePath, testenv.Login(e.t, e.db, a))
	// never read
	select {
	case <-written:
	case <-time.After(5 * time.Second):
		t.Fatal("the search is still writing to a client that stopped reading")
	}
}
