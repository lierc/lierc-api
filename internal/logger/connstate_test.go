package logger

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nsqio/go-nsq"
)

func body(s string) *nsq.Message { return &nsq.Message{Body: []byte(s)} }

// liercd's config changes are merged into the saved config; ones that
// can't be saved are dropped, and a database error is returned, so NSQ
// delivers the change again.
func TestHandleConfig(t *testing.T) {
	captureLog(t)
	l, store, _ := newTestLogger()
	if err := l.handleConfig(body(`{"Id":"c1","Config":{"Channels":["#a","#B"]}}`)); err != nil {
		t.Fatal(err)
	}
	for _, drop := range []string{`not json`, `{"Config":{"Nick":"x"}}`, `{"Id":"c1","Config":{}}`, `{"Id":"c1","Config":[1]}`} {
		if err := l.handleConfig(body(drop)); err != nil {
			t.Errorf("%s: %v, want dropped", drop, err)
		}
	}
	if !equal(store.saved(), []string{`c1 {"Channels":["#a","#B"]}`}) {
		t.Errorf("saved %q", store.saved())
	}

	store.setMergeFails(1)
	if err := l.handleConfig(body(`{"Id":"c1","Config":{"Nick":"y"}}`)); err == nil {
		t.Error("database error: returned nil, so it wouldn't be retried")
	}
	if err := l.handleConfig(body(`{"Id":"c1","Config":{"Nick":"y"}}`)); err != nil || len(store.saved()) != 2 {
		t.Errorf("retry: %v, saved %q", err, store.saved())
	}
}

// The config consumer retries a failed change for as long as it takes.
func TestRetryForeverConfig(t *testing.T) {
	c := retryForeverConfig()
	if c.MaxAttempts != 0 || c.DefaultRequeueDelay != reconnectInterval || c.LookupdPollInterval != reconnectInterval {
		t.Errorf("config %+v", c)
	}
}

// A status change goes to the Output with its owner; one for a connection
// that's gone is dropped silently, and one whose owner can't be looked up
// is dropped and logged. None is retried.
func TestHandleStatus(t *testing.T) {
	logs := captureLog(t)
	l, store, out := newTestLogger()
	for _, b := range []string{`{"Id":"c1","Connected":true,"Message":"Connected"}`, `{"Id":"gone","Connected":true}`, `nope`} {
		if err := l.handleStatus(body(b)); err != nil {
			t.Errorf("%s: %v", b, err)
		}
	}
	if len(out.statuses) != 1 || out.statuses[0] != (StatusChange{ConnectionId: "c1", Connected: true, UserId: "u1"}) {
		t.Errorf("statuses %+v", out.statuses)
	}

	l, store, out = newTestLogger()
	store.ownerErr = errors.New("database is down")
	if err := l.handleStatus(body(`{"Id":"c1","Connected":false}`)); err != nil || len(out.statuses) != 0 {
		t.Errorf("owner lookup failed: %v, %+v", err, out.statuses)
	}
	if s := logs.String(); !strings.Contains(s, "dropping status of c1: looking up its owner: database is down") {
		t.Errorf("logged %q", s)
	}
}

// PGOutput sends a status change as JSON on "status".
func TestPGOutputStatus(t *testing.T) {
	n := &fakeLoggedNotifier{}
	PGOutput{DB: n}.Status(&StatusChange{ConnectionId: "c1", Connected: true, UserId: "u1"})
	var got StatusChange
	if err := json.Unmarshal(n.status, &got); err != nil || got != (StatusChange{ConnectionId: "c1", Connected: true, UserId: "u1"}) {
		t.Errorf("sent %s (%v)", n.status, err)
	}
}
