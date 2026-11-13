package stream

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

func (e *env) config(id string) string {
	e.t.Helper()
	raw, _, err := e.db.LookupConfig(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(raw)
}

func (e *env) awaitConfig(id, want string) {
	e.t.Helper()
	poll(e.t, id+" config "+want, func() bool { return testenv.SameJSON(e.config(id), want) })
}

// liercd's changes on the config topic are merged into the saved config.
func TestConfigChangeSaved(t *testing.T) {
	addr, publish := testenv.NSQD(t)
	e := setup(t)
	e.s.NSQReconnect = 50 * time.Millisecond
	e.addConn("c1", e.addUser("alice"), `{"Host":"irc.x","Channels":["#old"]}`)
	consume(t, e.s, addr)
	publish("config", []byte(`{"Id":"c1","Config":{"Channels":["#a","#B"]}}`))
	e.awaitConfig("c1", `{"Host":"irc.x","Channels":["#a","#B"]}`)
}

// Messages that can't be saved are dropped, not retried.
func TestConfigChangeDrops(t *testing.T) {
	e := setup(t)
	e.addConn("c1", e.addUser("alice"), `{"Host":"irc.x"}`)
	for _, body := range []string{
		`not json`,
		`{"Config":{"Nick":"x"}}`,
		`{"Id":"c1","Config":{}}`,
		`{"Id":"c1","Config":[1]}`,
	} {
		if err := e.s.configChanged([]byte(body)); err != nil {
			t.Errorf("%s: %v, want dropped", body, err)
		}
	}
	if got := e.config("c1"); !testenv.SameJSON(got, `{"Host":"irc.x"}`) {
		t.Errorf("config changed to %s", got)
	}
}

// A change for a connection that's gone isn't an error.
func TestConfigChangeMissingConnection(t *testing.T) {
	e := setup(t)
	if err := e.s.configChanged([]byte(`{"Id":"zz","Config":{"Nick":"x"}}`)); err != nil {
		t.Errorf("missing connection: %v", err)
	}
}

// flaky fails its first fails MergeConfigs.
type flaky struct {
	db.Store
	mu    sync.Mutex
	fails int
}

func (f *flaky) MergeConfig(ctx context.Context, id string, fields []byte) error {
	f.mu.Lock()
	fail := f.fails > 0
	f.fails--
	f.mu.Unlock()
	if fail {
		return errors.New("database is down")
	}
	return f.Store.MergeConfig(ctx, id, fields)
}

// A database error is returned, so NSQ delivers the change again.
func TestConfigChangeRetried(t *testing.T) {
	addr, publish := testenv.NSQD(t)
	e := setup(t)
	e.s.NSQReconnect = 50 * time.Millisecond
	e.addConn("c1", e.addUser("alice"), `{"Host":"irc.x"}`)
	e.s.db = &flaky{Store: e.db, fails: 1}
	if err := e.s.configChanged([]byte(`{"Id":"c1","Config":{"Nick":"x"}}`)); err == nil {
		t.Fatal("database error: returned nil, so it wouldn't be retried")
	}
	e.s.db = &flaky{Store: e.db, fails: 1}
	consume(t, e.s, addr)
	publish("config", []byte(`{"Id":"c1","Config":{"Nick":"y"}}`))
	e.awaitConfig("c1", `{"Host":"irc.x","Nick":"y"}`)
}

// A database that's down for longer than go-nsq's default 5 attempts
// doesn't lose the change: it's retried until it's saved.
func TestConfigChangeRetriedPastMaxAttempts(t *testing.T) {
	addr, publish := testenv.NSQD(t)
	e := setup(t)
	e.s.NSQReconnect = 20 * time.Millisecond
	e.addConn("c1", e.addUser("alice"), `{"Host":"irc.x"}`)
	e.s.db = &flaky{Store: e.db, fails: 5}
	consume(t, e.s, addr)
	publish("config", []byte(`{"Id":"c1","Config":{"Nick":"y"}}`))
	e.awaitConfig("c1", `{"Host":"irc.x","Nick":"y"}`)
}

// The config channel is durable: a change published while the API is
// down is saved once it's back.
func TestConfigConsumerDurable(t *testing.T) {
	if configChannel != "lierc-api" {
		t.Fatalf("channel %q, want lierc-api", configChannel)
	}
	addr, publish := testenv.NSQD(t)
	e := setup(t)
	e.s.NSQReconnect = 50 * time.Millisecond
	e.addConn("c1", e.addUser("alice"), `{"Host":"irc.x"}`)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.s.Consume(ctx, addr) }()
	publish("config", []byte(`{"Id":"c1","Config":{"Nick":"one"}}`))
	e.awaitConfig("c1", `{"Host":"irc.x","Nick":"one"}`)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	publish("config", []byte(`{"Id":"c1","Config":{"Nick":"two"}}`))
	consume(t, e.s, addr)
	e.awaitConfig("c1", `{"Host":"irc.x","Nick":"two"}`)
}
