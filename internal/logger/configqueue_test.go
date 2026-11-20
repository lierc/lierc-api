package logger

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lierc/lierc/pkg/ircwire"
	"github.com/nsqio/go-nsq"
)

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Changes are saved in the order they were queued, a failing save retried
// until it succeeds before the next is tried.
func TestConfigQueueOrderAndRetry(t *testing.T) {
	fastRetries(t)
	captureLog(t)
	store := &fakeStore{}
	store.setMergeFails(3)
	var q configQueue
	q.start(store.MergeConfig)
	defer q.stop()
	q.queue("c1", map[string]any{"Channels": []string{"#a"}})
	q.queue("c1", map[string]any{"Channels": []string{"#a", "#b"}})
	q.queue("c2", map[string]any{"Nick": "x"})
	q.queue("c1", nil) // nothing to save

	want := []string{`c1 {"Channels":["#a"]}`, `c1 {"Channels":["#a","#b"]}`, `c2 {"Nick":"x"}`}
	eventually(t, "three saves", func() bool { return len(store.saved()) == 3 })
	if got := store.saved(); !equal(got, want) {
		t.Errorf("saved %q, want %q", got, want)
	}
}

// slowStore's MergeConfig waits until released, like a save waiting on a
// row lock.
type slowStore struct {
	fakeStore
	release chan struct{}
}

func (s *slowStore) MergeConfig(ctx context.Context, connection string, fields []byte) error {
	select {
	case <-s.release:
		return s.fakeStore.MergeConfig(ctx, connection, fields)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A save waiting on the row lock doesn't hold up the chats handler: the
// JOIN is stored and published while its config change waits.
func TestConfigSaveDoesNotBlockChats(t *testing.T) {
	l, base, out := newTestLogger()
	slow := &slowStore{release: make(chan struct{})}
	slow.owners = base.owners
	l.store = slow
	l.configs.stop()
	l.configs = configQueue{}
	l.configs.start(slow.MergeConfig)

	join := &ircwire.IRCClientMessage{Id: "c1", Message: &ircwire.IRCMessage{Command: "JOIN", Params: []string{"#b"},
		Prefix: &ircwire.IRCPrefix{Name: "me", Self: true}}, Config: map[string]any{"Channels": []string{"#a", "#b"}}}
	privmsg := &ircwire.IRCClientMessage{Id: "c1", Message: &ircwire.IRCMessage{Command: "PRIVMSG", Params: []string{"#b", "hi"},
		Prefix: &ircwire.IRCPrefix{Name: "bob"}}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, m := range []*ircwire.IRCClientMessage{join, privmsg} {
			if err := l.handleChat(clientMessage(t, m)); err != nil {
				t.Error(err)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("chats held up by a waiting config save")
	}
	if len(out.got) != 2 || len(slow.saved()) != 0 {
		t.Fatalf("published %d, saved %q before the lock was released", len(out.got), slow.saved())
	}
	close(slow.release)
	eventually(t, "the save", func() bool { return len(slow.saved()) == 1 })
	if got := slow.saved()[0]; got != `c1 {"Channels":["#a","#b"]}` {
		t.Errorf("saved %q", got)
	}
	l.configs.stop()
}

// stop saves what's queued; what it can't save within drainTimeout is
// logged as lost.
func TestConfigQueueStop(t *testing.T) {
	fastRetries(t)
	old := drainTimeout
	drainTimeout = 100 * time.Millisecond
	t.Cleanup(func() { drainTimeout = old })

	store := &fakeStore{}
	var q configQueue
	q.start(store.MergeConfig)
	q.queue("c1", map[string]any{"Nick": "a"})
	q.queue("c1", map[string]any{"Nick": "b"})
	q.stop()
	if got := store.saved(); len(got) != 2 {
		t.Errorf("saved %q at stop, want both", got)
	}

	logs := captureLog(t)
	var mu sync.Mutex
	failing := func(ctx context.Context, connection string, fields []byte) error {
		mu.Lock()
		defer mu.Unlock()
		return errors.New("database is down")
	}
	var q2 configQueue
	q2.start(failing)
	q2.queue("c2", map[string]any{"Nick": "c"})
	start := time.Now()
	q2.stop()
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("stop took %s", d)
	}
	if !strings.Contains(logs.String(), `config change for c2 lost at stop`) {
		t.Errorf("logged %q", logs.String())
	}
}

// clientMessage is m as it comes on chats.
func clientMessage(t *testing.T, m *ircwire.IRCClientMessage) *nsq.Message {
	t.Helper()
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return &nsq.Message{Body: body}
}

// A config change on a chats line is saved once the line is handled,
// whether the line is stored (our JOIN) or only passed on (our NICK in no
// channel); a line for a connection that's gone saves nothing.
func TestHandleChatConfig(t *testing.T) {
	l, store, out := newTestLogger()
	defer l.configs.stop()
	me := &ircwire.IRCPrefix{Name: "me", Self: true}
	for _, m := range []*ircwire.IRCClientMessage{
		{Id: "c1", Message: &ircwire.IRCMessage{Command: "JOIN", Params: []string{"#b"}, Prefix: me}, Config: map[string]any{"Channels": []string{"#b"}}},
		{Id: "c1", Message: &ircwire.IRCMessage{Command: "NICK", Params: []string{"me2"}, Prefix: me}, Channels: []string{}, Config: map[string]any{"Nick": "me2"}},
		{Id: "gone", Message: &ircwire.IRCMessage{Command: "PART", Params: []string{"#b"}, Prefix: me}, Config: map[string]any{"Channels": []string{}}},
	} {
		if err := l.handleChat(clientMessage(t, m)); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{`c1 {"Channels":["#b"]}`, `c1 {"Nick":"me2"}`}
	eventually(t, "two saves", func() bool { return len(store.saved()) == 2 })
	if got := store.saved(); !equal(got, want) {
		t.Errorf("saved %q, want %q", got, want)
	}
	if len(out.got) != 2 {
		t.Errorf("published %d, want the JOIN and the NICK", len(out.got))
	}
}

// When the owner can't be looked up, a line carrying a config change goes
// back to NSQ even if it's only passed on, so the change isn't lost.
func TestHandleChatConfigOwnerFails(t *testing.T) {
	captureLog(t)
	l, store, _ := newTestLogger()
	defer l.configs.stop()
	store.ownerErr = errors.New("database is down")
	nick := &ircwire.IRCClientMessage{Id: "c1", Channels: []string{}, Config: map[string]any{"Nick": "me2"},
		Message: &ircwire.IRCMessage{Command: "NICK", Params: []string{"me2"}, Prefix: &ircwire.IRCPrefix{Name: "me", Self: true}}}
	if err := l.handleChat(clientMessage(t, nick)); err == nil {
		t.Error("returned nil: the config change would be lost")
	}
}

// liercd's 000 status line is stored and passed on with its Status, for
// the stream server.
func TestHandleChatStatus(t *testing.T) {
	l, store, out := newTestLogger()
	defer l.configs.stop()
	line := &ircwire.IRCClientMessage{Id: "c1", Status: &ircwire.ConnectionStatus{Connected: true},
		Message: &ircwire.IRCMessage{Command: "000", Params: []string{"me", "Connected"}, Prefix: &ircwire.IRCPrefix{Name: "irc.x"}}}
	if err := l.handleChat(clientMessage(t, line)); err != nil {
		t.Fatal(err)
	}
	if len(store.messages) != 1 || len(out.got) != 1 || out.got[0].Status == nil || !out.got[0].Status.Connected {
		t.Errorf("stored %q, published %+v", store.messages, out.got)
	}
	b, _ := json.Marshal(out.got[0])
	if !strings.Contains(string(b), `"Status":{"Connected":true}`) {
		t.Errorf("payload %s", b)
	}
}
