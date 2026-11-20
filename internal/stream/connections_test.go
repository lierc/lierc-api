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

	"github.com/lierc/lierc-api/internal/db"

	"github.com/lierc/lierc-api/internal/testenv"
)

// changed announces a connection change as the API does.
func changed(e *env, user, conn, action string) {
	e.s.ConnectionChanged(context.Background(), db.ConnectionChange{User: user, Connection: conn, Action: action})
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

func TestConnectionChangedCreateUpdateDelete(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	e.liveConn("c1", u)
	c := e.openReplayed(u)

	// the server (Host, Port) comes along, for the UI's share links
	e.addConn("c2", u, `{"Host":"irc.two","Port":6697,"Alias":"Two","Nick":"me2"}`)
	changed(e, u, "c2", "create")
	if ev := nextConnEvent(c); ev.typ != "connection:create" || ev.data != `{"Id":"c2","Name":"Two","Nick":"me2","Host":"irc.two","Port":6697}` || ev.hasID {
		t.Errorf("create: %+v", ev)
	}

	if _, err := e.db.Pool.Exec(context.Background(), `UPDATE connections SET config = '{"Host":"irc.deux","Port":6667,"Alias":"","Nick":"me2"}' WHERE id = 'c2'`); err != nil {
		t.Fatal(err)
	}
	changed(e, u, "c2", "update")
	if ev := nextConnEvent(c); ev.typ != "connection:update" || ev.data != `{"Id":"c2","Name":"irc.deux","Host":"irc.deux","Port":6667}` {
		t.Errorf("update: %+v", ev)
	}

	// the row is gone by the time a delete is announced
	if _, err := e.db.Pool.Exec(context.Background(), `DELETE FROM connections WHERE id = 'c2'`); err != nil {
		t.Fatal(err)
	}
	changed(e, u, "c2", "delete")
	if ev := nextConnEvent(c); ev.typ != "connection:delete" || ev.data != `{"Id":"c2"}` {
		t.Errorf("delete: %+v", ev)
	}
	c.checkErr()
}

// Another user's streams hear nothing.
func TestConnectionChangedOnlyOwner(t *testing.T) {
	e := setup(t)
	a, b := e.addUser("alice"), e.addUser("bob")
	e.liveConn("ca", a)
	e.liveConn("cb", b)
	ca, cb := e.openReplayed(a), e.openReplayed(b)
	changed(e, a, "ca", "delete")
	changed(e, b, "cb", "delete")
	if ev := nextConnEvent(ca); ev.data != `{"Id":"ca"}` {
		t.Errorf("alice got %+v", ev)
	}
	if ev := nextConnEvent(cb); ev.data != `{"Id":"cb"}` {
		t.Errorf("bob got %+v first", ev)
	}
}

// A change for a user with no streams is dropped quietly.
func TestConnectionChangedNoStreams(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	e.addConn("c1", u, `{"Host":"irc.one"}`)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(io.Discard) })
	changed(e, u, "c1", "create")
	changed(e, u, "c1", "delete")
	time.Sleep(300 * time.Millisecond)
	if logs.Len() != 0 {
		t.Errorf("logged %q", logs.String())
	}
}

// create or update for a row that's already gone sends nothing.
func TestConnectionChangedRowGone(t *testing.T) {
	e := setup(t)
	u := e.addUser("alice")
	e.liveConn("c1", u)
	c := e.openReplayed(u)
	changed(e, u, "gone", "create")
	changed(e, u, "gone", "update")
	changed(e, u, "c1", "delete") // the sentinel: it must be the first event
	if ev := nextConnEvent(c); ev.typ != "connection:delete" || ev.data != `{"Id":"c1"}` {
		t.Errorf("first event %+v, want the sentinel delete", ev)
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
		e.s.Deliver(e.owned([]byte(fmt.Sprintf(`{"ConnectionId":"c1","Message":{"Command":%q,"Params":["x"]}}`, cmd))))
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

// Deliver goes by UserId alone: a message goes to its owner's streams
// even for a connection that isn't in the database, and one without a
// UserId is dropped, though its connection has an owner.
func TestDeliverByUserId(t *testing.T) {
	e := setup(t)
	a, b := e.addUser("alice"), e.addUser("bob")
	e.liveConn("cb", b)
	ca, cb := e.openReplayed(a), e.openReplayed(b)

	withOwner := fmt.Sprintf(`{"ConnectionId":"nowhere","MessageId":5,"UserId":%q,"Message":{"Command":"PRIVMSG"}}`, a)
	e.s.Deliver([]byte(withOwner))
	e.s.Deliver([]byte(`{"ConnectionId":"cb","MessageId":6,"Message":{"Command":"PRIVMSG"}}`)) // no owner: dropped
	e.s.Deliver([]byte(fmt.Sprintf(`{"ConnectionId":"cb","MessageId":7,"UserId":%q,"Message":{"Command":"PRIVMSG"}}`, b)))

	if ev := ca.nextIRC(); ev.data != withOwner || ev.id != "5" {
		t.Errorf("alice got %+v", ev)
	}
	// bob's first event is 7: 6, without its owner, reached nobody
	if ev := cb.nextIRC(); ev.id != "7" {
		t.Errorf("bob got %+v, want id 7", ev)
	}
}
