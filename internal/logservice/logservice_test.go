package logservice

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/logger"
	"github.com/lierc/lierc-api/internal/testenv"
	"github.com/lierc/lierc/pkg/ircwire"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// memOutput is an Output in memory, as lierc-api's stream server could
// be.
type memOutput struct {
	mu  sync.Mutex
	got []*logger.LoggedMessage
}

func (o *memOutput) Logged(e *logger.LoggedMessage) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.got = append(o.got, e)
}

func (o *memOutput) Status(*logger.StatusChange) {}

func (o *memOutput) messages() []*logger.LoggedMessage {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]*logger.LoggedMessage(nil), o.got...)
}

// memStreams counts streams in memory, and records who was asked about.
type memStreams struct {
	mu    sync.Mutex
	asked []string
}

func (s *memStreams) StreamCount(ctx context.Context, user string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, user)
	return 1, nil // online: no notification is sent
}

func (s *memStreams) users() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The service run with its Options replaced, as another process would run
// it: a highlight from chats is stored, goes to the Output, and the
// notifier asks Streams whether its user is online.
func TestServiceWithOptions(t *testing.T) {
	d := testenv.Postgres(t)
	testenv.AddConnection(t, d, "c1", "u1", `{"Highlight": ["leedo"]}`)
	nsqd, publish := testenv.NSQD(t)
	env := map[string]string{"NSQD_HOST": nsqd}

	out, streams := &memOutput{}, &memStreams{}
	svc, err := New(func(k string) string { return env[k] }, d, Options{Output: out, Streams: streams})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			svc.Stop()
		}
	}()

	body, _ := json.Marshal(ircwire.IRCClientMessage{Id: "c1", Message: &ircwire.IRCMessage{
		Command: "PRIVMSG", Params: []string{"#a", "hi leedo"}, Prefix: &ircwire.IRCPrefix{Name: "bob"},
		Time: float64(time.Now().Unix())}})
	publish("chats", body)

	waitFor(t, "the output", func() bool { return len(out.messages()) == 1 })
	if e := out.messages()[0]; e.ConnectionId != "c1" || e.MessageId == 0 || !e.Highlight || e.UserId != "u1" {
		t.Errorf("output got %+v", e)
	}
	waitFor(t, "the stream count", func() bool { return len(streams.users()) == 1 })
	if u := streams.users()[0]; u != "u1" {
		t.Errorf("asked about %q, want u1", u)
	}
	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM highlights WHERE connection_id = 'c1'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("highlights stored: %d, %v", n, err)
	}

	stopped = true
	done := make(chan struct{})
	go func() { svc.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop didn't return")
	}
}

// Without Options, the service sends each message as a NOTIFY on
// "logged", which a ListenStream gets, with its owner.
func TestServiceDefaults(t *testing.T) {
	d := testenv.Postgres(t)
	testenv.AddConnection(t, d, "c1", "u1", `{}`)
	nsqd, publish := testenv.NSQD(t)
	svc, err := New(func(k string) string { return map[string]string{"NSQD_HOST": nsqd}[k] }, d, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if svc.notifier == nil || svc.notifier.Streams != nil {
		t.Fatalf("service %+v", svc)
	}

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan []byte, 10)
	listening := make(chan struct{})
	go func() {
		defer close(listening)
		d.ListenStream(ctx, 10*time.Millisecond, func(p []byte) { got <- p }, func([]byte) {}, func(error) {})
	}()
	defer func() { cancel(); <-listening }()

	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer svc.Stop()

	// publish until the listener is in place: NOTIFYs before it are missed
	body, _ := json.Marshal(ircwire.IRCClientMessage{Id: "c1", Message: &ircwire.IRCMessage{
		Command: "TAGMSG", Params: []string{"#a"}, Prefix: &ircwire.IRCPrefix{Name: "bob"}}})
	var payload []byte
	waitFor(t, "a logged NOTIFY", func() bool {
		publish("chats", body)
		select {
		case payload = <-got:
			return true
		case <-time.After(100 * time.Millisecond):
			return false
		}
	})
	var e logger.LoggedMessage
	if err := json.Unmarshal(payload, &e); err != nil {
		t.Fatal(err)
	}
	if e.ConnectionId != "c1" || e.UserId != "u1" || e.Message.Command != "TAGMSG" {
		t.Errorf("got %s", payload)
	}
}

// The service saves liercd's config changes, durably: one published while
// it's stopped is saved once it's back. A status change comes out as a
// NOTIFY on "status", with its owner.
func TestServiceConfigAndStatus(t *testing.T) {
	d := testenv.Postgres(t)
	testenv.AddConnection(t, d, "c1", "u1", `{"Host":"irc.x"}`)
	nsqd, publish := testenv.NSQD(t)
	getenv := func(k string) string { return map[string]string{"NSQD_HOST": nsqd}[k] }
	config := func() string {
		raw, _, err := d.LookupConfig(context.Background(), "c1")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	statuses := make(chan []byte, 100)
	listening := make(chan struct{})
	go func() {
		defer close(listening)
		d.ListenStream(ctx, 10*time.Millisecond, func([]byte) {}, func(p []byte) { statuses <- p }, func(error) {})
	}()
	defer func() { cancel(); <-listening }()

	start := func() *Service {
		svc, err := New(getenv, d, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Start(ctx); err != nil {
			t.Fatal(err)
		}
		return svc
	}

	svc := start()
	publish("config", []byte(`{"Id":"c1","Config":{"Nick":"one"}}`))
	waitFor(t, "the first config change", func() bool { return testenv.SameJSON(config(), `{"Host":"irc.x","Nick":"one"}`) })

	var payload []byte
	waitFor(t, "a status NOTIFY", func() bool {
		publish("status", []byte(`{"Id":"c1","Connected":true,"Message":"Connected"}`))
		select {
		case payload = <-statuses:
			return true
		case <-time.After(100 * time.Millisecond):
			return false
		}
	})
	var s logger.StatusChange
	if err := json.Unmarshal(payload, &s); err != nil || s != (logger.StatusChange{ConnectionId: "c1", Connected: true, UserId: "u1"}) {
		t.Errorf("status %s (%v)", payload, err)
	}

	svc.Stop()
	publish("config", []byte(`{"Id":"c1","Config":{"Nick":"two"}}`))
	svc = start()
	defer svc.Stop()
	waitFor(t, "the change published while stopped", func() bool { return testenv.SameJSON(config(), `{"Host":"irc.x","Nick":"two"}`) })
}
