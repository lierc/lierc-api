package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/testenv"
)

// listen runs ListenConnections until the test ends, returning once its
// first LISTEN is active; each later (re)LISTEN is sent on the channel.
func listen(t *testing.T, e *env) chan struct{} {
	t.Helper()
	ready := make(chan struct{}, 10)
	e.s.listening = func() { ready <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.s.ListenConnections(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-ready:
	case <-time.After(waitFor):
		t.Fatal("never listening")
	}
	return ready
}

func notify(t *testing.T, e *env, user, conn, action string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"user": user, "connection": conn, "action": action})
	notifyRaw(t, e, string(payload))
}

func notifyRaw(t *testing.T, e *env, payload string) {
	t.Helper()
	if _, err := e.db.Pool.Exec(context.Background(), `SELECT pg_notify('connections', $1)`, payload); err != nil {
		t.Fatal(err)
	}
}

// nextConnEvent is the client's next connection event, skipping pings.
func nextConnEvent(c *client) event {
	c.t.Helper()
	for {
		ev := c.next()
		if strings.HasPrefix(ev.typ, "connection") {
			return ev
		}
	}
}

func TestConnectionNotifyCreateUpdateDelete(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	e.liveConn("c1", u)
	c := e.openReplayed(u)
	listen(t, e)

	e.addConn("c2", u, `{"Host":"irc.two","Alias":"Two","Nick":"me2"}`)
	notify(t, e, u, "c2", "create")
	if ev := nextConnEvent(c); ev.typ != "connection:create" || ev.data != `{"Id":"c2","Name":"Two","Nick":"me2"}` || ev.hasID {
		t.Errorf("create: %+v", ev)
	}

	if _, err := e.db.Pool.Exec(context.Background(), `UPDATE connection SET config = '{"Host":"irc.deux","Alias":"","Nick":"me2"}' WHERE id = 'c2'`); err != nil {
		t.Fatal(err)
	}
	notify(t, e, u, "c2", "update")
	if ev := nextConnEvent(c); ev.typ != "connection:update" || ev.data != `{"Id":"c2","Name":"irc.deux"}` {
		t.Errorf("update: %+v", ev)
	}

	// the row is gone by the time a delete is announced
	if _, err := e.db.Pool.Exec(context.Background(), `DELETE FROM connection WHERE id = 'c2'`); err != nil {
		t.Fatal(err)
	}
	notify(t, e, u, "c2", "delete")
	if ev := nextConnEvent(c); ev.typ != "connection:delete" || ev.data != `{"Id":"c2"}` {
		t.Errorf("delete: %+v", ev)
	}
	c.checkErr()
}

// Another user's streams hear nothing.
func TestConnectionNotifyOnlyOwner(t *testing.T) {
	e := setup(t)
	a, b := e.addUser("alice"), e.addUser("bob")
	e.liveConn("ca", a)
	e.liveConn("cb", b)
	ca, cb := e.openReplayed(a), e.openReplayed(b)
	listen(t, e)
	notify(t, e, a, "ca", "delete")
	notify(t, e, b, "cb", "delete")
	if ev := nextConnEvent(ca); ev.data != `{"Id":"ca"}` {
		t.Errorf("alice got %+v", ev)
	}
	if ev := nextConnEvent(cb); ev.data != `{"Id":"cb"}` {
		t.Errorf("bob got %+v first", ev)
	}
}

// A notification for a user with no streams is dropped quietly.
func TestConnectionNotifyNoStreams(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	e.addConn("c1", u, `{"Host":"irc.one"}`)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(io.Discard) })
	listen(t, e)
	notify(t, e, u, "c1", "create")
	notify(t, e, u, "c1", "delete")
	time.Sleep(300 * time.Millisecond)
	if logs.Len() != 0 {
		t.Errorf("logged %q", logs.String())
	}
}

// create or update for a row that's already gone sends nothing.
func TestConnectionNotifyRowGone(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	e.liveConn("c1", u)
	c := e.openReplayed(u)
	listen(t, e)
	notify(t, e, u, "gone", "create")
	notify(t, e, u, "gone", "update")
	notify(t, e, u, "c1", "delete") // the sentinel: it must be the first event
	if ev := nextConnEvent(c); ev.typ != "connection:delete" || ev.data != `{"Id":"c1"}` {
		t.Errorf("first event %+v, want the sentinel delete", ev)
	}
}

// Bad payloads are skipped and the listener carries on.
func TestConnectionNotifyMalformed(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	e.liveConn("c1", u)
	c := e.openReplayed(u)
	listen(t, e)
	notifyRaw(t, e, "not json")
	notify(t, e, u, "c1", "explode")
	notify(t, e, u, "c1", "delete")
	if ev := nextConnEvent(c); ev.typ != "connection:delete" || ev.data != `{"Id":"c1"}` {
		t.Errorf("first event %+v, want the delete", ev)
	}
}

// After the listening connection is killed, the listener reconnects and
// later notifications still arrive.
func TestListenReconnects(t *testing.T) {
	e := setup(t)
	e.s.NSQReconnect = 50 * time.Millisecond
	u := e.addUser("alice")
	e.liveConn("c1", u)
	c := e.openReplayed(u)
	ready := listen(t, e)
	if _, err := e.db.Pool.Exec(context.Background(),
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query LIKE 'LISTEN %' AND pid <> pg_backend_pid()`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(waitFor):
		t.Fatal("never listened again")
	}
	notify(t, e, u, "c1", "delete")
	if ev := nextConnEvent(c); ev.typ != "connection:delete" {
		t.Errorf("after reconnect: %+v", ev)
	}
}

// liercd's fake CREATE and DELETE never reach the UI; an old liercd's
// CONNECT and DISCONNECT still pass through as irc events.
func TestDeliverDropsCreateDeletePassesConnect(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	e.liveConn("c1", u)
	c := e.openReplayed(u)
	for _, cmd := range []string{"CREATE", "DELETE", "CONNECT", "DISCONNECT"} {
		e.s.Deliver([]byte(fmt.Sprintf(`{"ConnectionId":"c1","Message":{"Command":%q,"Params":["x"]}}`, cmd)))
	}
	var got []string
	for range 2 {
		ev := c.nextIRC()
		var m struct{ Message struct{ Command string } }
		json.Unmarshal([]byte(ev.data), &m)
		got = append(got, m.Message.Command)
	}
	if strings.Join(got, ",") != "CONNECT,DISCONNECT" {
		t.Errorf("delivered %v", got)
	}
}

// liercd's status topic becomes connection:connect / :disconnect for the
// owner, with no SSE id.
func TestStatusTopicEvents(t *testing.T) {
	addr, publish := testenv.NSQD(t)
	e := setup(t)
	u := e.addUser("alice")
	e.liveConn("c1", u)
	c := e.openReplayed(u)
	consume(t, e.s, addr)

	deadline := time.Now().Add(waitFor)
	for seen := false; !seen; {
		if time.Now().After(deadline) {
			t.Fatal("status consumer never subscribed")
		}
		publish("status", []byte(`{"Id":"c1","Connected":false,"Message":"Closing connection"}`))
		select {
		case ev := <-c.events:
			if ev.typ == "connection:disconnect" {
				seen = true
				if ev.data != `{"Id":"c1"}` || ev.hasID {
					t.Errorf("disconnect: %+v", ev)
				}
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	publish("status", []byte(`{"Id":"nobody","Connected":true}`))
	publish("status", []byte(`{"Id":"c1","Connected":true,"Message":"Connected"}`))
	for {
		ev := nextConnEvent(c)
		if ev.typ == "connection:disconnect" {
			continue // a leftover from the subscribe loop
		}
		if ev.typ != "connection:connect" || ev.data != `{"Id":"c1"}` {
			t.Errorf("connect: %+v", ev)
		}
		break
	}
}
