package api

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/testenv"
)

// liveEvents opens user's stream and sends each event's type and data.
func (e *env) liveEvents(user string) <-chan [2]string {
	e.t.Helper()
	res := e.do("GET", "/events", user)
	if res.StatusCode != 200 {
		e.t.Fatalf("stream: %d", res.StatusCode)
	}
	e.t.Cleanup(func() { res.Body.Close() })
	out := make(chan [2]string, 100)
	go func() {
		defer close(out)
		br := bufio.NewReader(res.Body)
		typ := ""
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				return
			}
			l = strings.TrimSuffix(l, "\n")
			if strings.HasPrefix(l, "event: ") {
				typ = l[7:]
			} else if strings.HasPrefix(l, "data: ") && strings.HasPrefix(typ, "connection:") {
				out <- [2]string{typ, l[6:]}
			}
		}
	}()
	return out
}

func waitEvent(t *testing.T, ch <-chan [2]string, typ, id string) map[string]any {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("stream ended waiting for %s", typ)
			}
			var d map[string]any
			json.Unmarshal([]byte(ev[1]), &d)
			if ev[0] == typ && d["Id"] == id {
				return d
			}
		case <-deadline:
			t.Fatalf("no %s for %s", typ, id)
		}
	}
}

// API changes reach an open stream with no LISTEN running: the API calls
// the stream server in memory, not through pg_notify.
func TestConnectionEventsWithoutNotify(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	ch := e.liveEvents(a)
	time.Sleep(200 * time.Millisecond) // the stream registers

	_, body := e.send("POST", "/connection", a, `{"Host":"irc.example","Port":6667,"Nick":"me"}`)
	var res struct{ Id string }
	json.Unmarshal([]byte(body), &res)
	if res.Id == "" {
		t.Fatalf("create: %s", body)
	}
	waitEvent(t, ch, "connection:create", res.Id)
	e.sendWant("PUT", "/connection/"+res.Id+"/config", a, `{"Alias":"Net"}`, 200, `{"status":"ok"}`)
	if d := waitEvent(t, ch, "connection:update", res.Id); d["Name"] != "Net" {
		t.Errorf("update = %v", d)
	}
	e.fake.Respond("POST", "/"+res.Id+"/destroy", 200, "ok")
	e.sendWant("DELETE", "/connection/"+res.Id, a, "", 200, `{"status":"ok"}`)
	waitEvent(t, ch, "connection:delete", res.Id)
}
