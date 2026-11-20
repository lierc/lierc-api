package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/lierc/lierc/pkg/ircwire"
	"github.com/nsqio/go-nsq"
)

type fakeStore struct {
	messages []string // "connection channel command highlight"
	privates []string // "connection nick"
	err      error
	nextId   int
	// owners are the connections' owners; Owner fails with ownerErr, and
	// counts its calls in lookups
	owners   map[string]string
	ownerErr error
	lookups  int
	// configs are MergeConfig's calls; mergeFails fails that many first
	configs    []string
	mergeFails int
}

// MergeConfig records "connection fields", failing mergeFails times first.
func (s *fakeStore) MergeConfig(ctx context.Context, connection string, fields []byte) error {
	if s.mergeFails > 0 {
		s.mergeFails--
		return errors.New("database is down")
	}
	s.configs = append(s.configs, connection+" "+string(fields))
	return nil
}

func (s *fakeStore) Owner(ctx context.Context, connection string) (string, bool, error) {
	s.lookups++
	if s.ownerErr != nil {
		return "", false, s.ownerErr
	}
	u, ok := s.owners[connection]
	return u, ok, nil
}

func (s *fakeStore) InsertMessage(ctx context.Context, connection string, m *ircwire.IRCMessage, channels []string, highlight bool) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	for _, channel := range channels {
		s.nextId++
		s.messages = append(s.messages, connection+" "+channel+" "+m.Command+map[bool]string{true: " highlight", false: ""}[highlight])
	}
	return s.nextId, nil
}

func (s *fakeStore) InsertPrivate(ctx context.Context, connection string, nick string, time float64) error {
	if s.err != nil {
		return s.err
	}
	s.privates = append(s.privates, connection+" "+nick)
	return nil
}

func (s *fakeStore) Highlights(ctx context.Context) (map[string]string, error) {
	return nil, nil
}

// fakeOutput records what the logger hands its Output.
type fakeOutput struct {
	published []string // "logged command id"
	got       []*LoggedMessage
	statuses  []StatusChange
}

func (o *fakeOutput) Logged(e *LoggedMessage) {
	o.published = append(o.published, "logged "+e.Message.Command+" "+string(rune('0'+e.MessageId)))
	o.got = append(o.got, e)
}

func (o *fakeOutput) Status(s *StatusChange) { o.statuses = append(o.statuses, *s) }

// fakeNotifier records what the logger hands to the notifier.
type fakeNotifier struct{ got []string } // "command id"

func (f *fakeNotifier) Notify(e *LoggedMessage) {
	f.got = append(f.got, e.Message.Command+" "+string(rune('0'+e.MessageId)))
}

// newTestLogger's notifier is a *fakeNotifier: notified(l) reads it.
func newTestLogger() (*Logger, *fakeStore, *fakeOutput) {
	store := &fakeStore{owners: map[string]string{"c1": "u1"}}
	pub := &fakeOutput{}
	h := &Highlighters{}
	h.Replace(map[string]*regexp.Regexp{"c1": regexp.MustCompile(`(?i)\bleedo\b`)})
	return &Logger{store: store, out: pub, highlights: h, notifier: &fakeNotifier{}}, store, pub
}

func notified(l *Logger) []string { return l.notifier.(*fakeNotifier).got }

func chat(t *testing.T, m *ircwire.IRCMessage) *nsq.Message {
	body, err := json.Marshal(ircwire.IRCClientMessage{Id: "c1", Message: m})
	if err != nil {
		t.Fatal(err)
	}
	return &nsq.Message{Body: body}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestHandleChat(t *testing.T) {
	prefix := func(name string) *ircwire.IRCPrefix { return &ircwire.IRCPrefix{Name: name} }

	tests := []struct {
		name      string
		m         *ircwire.IRCMessage
		stored    []string
		privates  []string
		published []string
		notified  []string
	}{
		{"channel message",
			&ircwire.IRCMessage{Command: "PRIVMSG", Params: []string{"#chan", "hi"}, Prefix: prefix("bob")},
			[]string{"c1 #chan PRIVMSG"}, nil, []string{"logged PRIVMSG 1"}, nil},
		{"highlight",
			&ircwire.IRCMessage{Command: "PRIVMSG", Params: []string{"#chan", "hi leedo"}, Prefix: prefix("bob")},
			[]string{"c1 #chan PRIVMSG highlight"}, nil, []string{"logged PRIVMSG 1"}, []string{"PRIVMSG 1"}},
		{"private message",
			&ircwire.IRCMessage{Command: "PRIVMSG", Params: []string{"me", "psst"}, Prefix: prefix("bob"), Direct: true},
			[]string{"c1 bob PRIVMSG"}, []string{"c1 bob"}, []string{"logged PRIVMSG 1"}, []string{"PRIVMSG 1"}},
		{"typing",
			&ircwire.IRCMessage{Command: "TAGMSG", Params: []string{"#chan"}, Tags: map[string]string{"+typing": "active"}, Prefix: prefix("bob")},
			nil, nil, []string{"logged TAGMSG 0"}, nil},
		{"nick without channels (older liercd)",
			&ircwire.IRCMessage{Command: "NICK", Params: []string{"robert"}, Prefix: prefix("bob")},
			nil, nil, nil, nil},
	}

	for _, tt := range tests {
		l, store, pub := newTestLogger()
		if err := l.handleChat(chat(t, tt.m)); err != nil {
			t.Errorf("%s: handleChat returned %v", tt.name, err)
		}
		if !equal(store.messages, tt.stored) || !equal(store.privates, tt.privates) || !equal(pub.published, tt.published) || !equal(notified(l), tt.notified) {
			t.Errorf("%s:\n  stored %q, want %q\n  privates %q, want %q\n  published %q, want %q\n  notified %q, want %q",
				tt.name, store.messages, tt.stored, store.privates, tt.privates, pub.published, tt.published, notified(l), tt.notified)
		}
	}
}

func TestHandleChatOwnPrivateMessage(t *testing.T) {
	l, _, pub := newTestLogger()
	m := &ircwire.IRCMessage{Command: "PRIVMSG", Params: []string{"bob", "psst"}, Prefix: &ircwire.IRCPrefix{Name: "me", Self: true}, Direct: true}
	if err := l.handleChat(chat(t, m)); err != nil {
		t.Fatal(err)
	}
	if !equal(pub.published, []string{"logged PRIVMSG 1"}) || notified(l) != nil {
		t.Fatalf("published %q, notified %q", pub.published, notified(l))
	}
}

func TestHandleChatNotificationsOff(t *testing.T) {
	l, _, pub := newTestLogger()
	l.notifier = nil
	m := &ircwire.IRCMessage{Command: "PRIVMSG", Params: []string{"#chan", "hi leedo"}, Prefix: &ircwire.IRCPrefix{Name: "bob"}}
	if err := l.handleChat(chat(t, m)); err != nil {
		t.Fatal(err)
	}
	if !equal(pub.published, []string{"logged PRIVMSG 1"}) {
		t.Fatalf("published %q", pub.published)
	}
}

func TestHandleChatErrors(t *testing.T) {
	m := &ircwire.IRCMessage{Command: "PRIVMSG", Params: []string{"#chan", "hi"}, Prefix: &ircwire.IRCPrefix{Name: "bob"}}

	// unreadable messages are dropped, not retried
	l, store, pub := newTestLogger()
	if err := l.handleChat(&nsq.Message{Body: []byte("{not json")}); err != nil {
		t.Errorf("unreadable message: returned %v, want nil", err)
	}
	if err := l.handleChat(&nsq.Message{Body: []byte(`{"Id":"c1"}`)}); err != nil {
		t.Errorf("message without Message: returned %v, want nil", err)
	}
	if len(store.messages)+len(pub.published) != 0 {
		t.Errorf("unreadable messages were stored or published")
	}

	// database errors are returned so NSQ retries, and nothing is published
	l, store, pub = newTestLogger()
	store.err = errors.New("database is down")
	if err := l.handleChat(chat(t, m)); err == nil {
		t.Errorf("database error: returned nil, want an error")
	}
	if len(pub.published) != 0 {
		t.Errorf("published %q after a database error", pub.published)
	}

	// a connection deleted after its owner was cached: the insert finds
	// none, so the message is dropped and the connection forgotten
	l, store, pub = newTestLogger()
	l.handleChat(chat(t, m)) // caches c1's owner
	store.err = ErrNoConnection
	if err := l.handleChat(chat(t, m)); err != nil {
		t.Errorf("deleted connection: returned %v, want nil", err)
	}
	if !equal(pub.published, []string{"logged PRIVMSG 1"}) {
		t.Errorf("deleted connection: published %q, want only the first", pub.published)
	}
	if _, cached := l.owners.m["c1"]; cached {
		t.Error("deleted connection still cached")
	}
}

// NICK and QUIT arrive on chats with their channels: stored in each,
// published once with the last id.
func TestHandleChatMulti(t *testing.T) {
	l, store, pub := newTestLogger()
	body, _ := json.Marshal(ircwire.IRCClientMessage{
		Id:       "c1",
		Message:  &ircwire.IRCMessage{Command: "QUIT", Params: []string{"bye"}, Prefix: &ircwire.IRCPrefix{Name: "bob"}},
		Channels: []string{"#a", "#b"},
	})

	if err := l.handleChat(&nsq.Message{Body: body}); err != nil {
		t.Fatalf("handleChat returned %v", err)
	}
	if !equal(store.messages, []string{"c1 #a QUIT", "c1 #b QUIT"}) {
		t.Errorf("stored %q", store.messages)
	}
	if !equal(pub.published, []string{"logged QUIT 2"}) {
		t.Errorf("published %q, want the last id once", pub.published)
	}

	store.err = errors.New("database is down")
	if err := l.handleChat(&nsq.Message{Body: body}); err == nil {
		t.Errorf("database error: returned nil, want an error")
	}
}

// A NICK that touched no channel (your own, in none) is stored nowhere
// but still published.
func TestHandleChatMultiNoChannels(t *testing.T) {
	l, store, pub := newTestLogger()
	body := []byte(`{"Id":"c1","Message":{"Command":"NICK","Params":["robert"],"Prefix":{"Name":"bob"}},"Channels":[]}`)

	if err := l.handleChat(&nsq.Message{Body: body}); err != nil {
		t.Fatalf("handleChat returned %v", err)
	}
	if len(store.messages) != 0 {
		t.Errorf("stored %q, want nothing", store.messages)
	}
	if !equal(pub.published, []string{"logged NICK 0"}) {
		t.Errorf("published %q", pub.published)
	}
}

// An ordinary line, which liercd sends with "Channels":null, is handled
// as before.
func TestHandleChatPlainUnchanged(t *testing.T) {
	l, store, pub := newTestLogger()
	body := []byte(`{"Id":"c1","Message":{"Command":"PRIVMSG","Params":["#chan","hi"],"Prefix":{"Name":"bob"}},"Channels":null}`)

	if err := l.handleChat(&nsq.Message{Body: body}); err != nil {
		t.Fatalf("handleChat returned %v", err)
	}
	if !equal(store.messages, []string{"c1 #chan PRIVMSG"}) || !equal(pub.published, []string{"logged PRIVMSG 1"}) {
		t.Errorf("stored %q, published %q", store.messages, pub.published)
	}
}

// Every message published carries its connection's owner, stored or not,
// looked up once per connection.
func TestHandleChatUserId(t *testing.T) {
	l, store, out := newTestLogger()
	for _, m := range []*nsq.Message{chat(t, stored()), chat(t, passed()), nick("c1", `["#a"]`), nick("c1", `[]`)} {
		if err := l.handleChat(m); err != nil {
			t.Fatal(err)
		}
	}
	if len(out.got) != 4 {
		t.Fatalf("published %d messages, want 4", len(out.got))
	}
	for _, e := range out.got {
		if e.UserId != "u1" {
			t.Errorf("%s: owner %q, want u1", e.Message.Command, e.UserId)
		}
	}
	if store.lookups != 1 {
		t.Errorf("%d lookups for one connection, want 1", store.lookups)
	}
}

func stored() *ircwire.IRCMessage {
	return &ircwire.IRCMessage{Command: "PRIVMSG", Params: []string{"#chan", "hi"}, Prefix: &ircwire.IRCPrefix{Name: "bob"}}
}

func passed() *ircwire.IRCMessage {
	return &ircwire.IRCMessage{Command: "TAGMSG", Params: []string{"#chan"}, Prefix: &ircwire.IRCPrefix{Name: "bob"}}
}

func nick(connection, channels string) *nsq.Message {
	return &nsq.Message{Body: []byte(`{"Id":"` + connection + `","Message":{"Command":"NICK","Params":["robert"],"Prefix":{"Name":"bob"}},"Channels":` + channels + `}`)}
}

func on(t *testing.T, connection string, m *ircwire.IRCMessage) *nsq.Message {
	body, err := json.Marshal(ircwire.IRCClientMessage{Id: connection, Message: m})
	if err != nil {
		t.Fatal(err)
	}
	return &nsq.Message{Body: body}
}

// A connection that doesn't exist: every kind of message is dropped,
// silently, nothing stored, and the miss isn't cached.
func TestHandleChatNoConnection(t *testing.T) {
	logs := captureLog(t)
	l, store, out := newTestLogger()
	for _, m := range []*nsq.Message{on(t, "gone", stored()), on(t, "gone", passed()), nick("gone", `["#a"]`), nick("gone", `[]`)} {
		if err := l.handleChat(m); err != nil {
			t.Errorf("returned %v, want nil", err)
		}
	}
	if len(out.got)+len(store.messages)+len(store.privates) != 0 || len(notified(l)) != 0 {
		t.Errorf("published %q, stored %q %q", out.published, store.messages, store.privates)
	}
	if store.lookups != 4 {
		t.Errorf("%d lookups, want 4: a miss isn't cached", store.lookups)
	}
	if s := logs.String(); s != "" {
		t.Errorf("logged %q, want nothing", s)
	}
}

// When the owner can't be looked up, a message to store goes back to NSQ
// (an error) before anything is stored; one only passed on is dropped,
// and logged. Neither is published, and nothing is cached.
func TestHandleChatOwnerLookupFails(t *testing.T) {
	logs := captureLog(t)
	l, store, out := newTestLogger()
	store.ownerErr = errors.New("database is down")
	for _, m := range []*nsq.Message{chat(t, stored()), nick("c1", `["#a"]`)} {
		if err := l.handleChat(m); err == nil {
			t.Error("message to store: returned nil, want an error to retry")
		}
	}
	for _, m := range []*nsq.Message{chat(t, passed()), nick("c1", `[]`)} {
		if err := l.handleChat(m); err != nil {
			t.Errorf("message passed on: returned %v, want nil", err)
		}
	}
	if len(out.got)+len(store.messages) != 0 {
		t.Errorf("published %q, stored %q", out.published, store.messages)
	}
	if !strings.Contains(logs.String(), "dropping TAGMSG on c1: looking up its owner: database is down") {
		t.Errorf("logged %q", logs.String())
	}

	store.ownerErr = nil
	if err := l.handleChat(chat(t, passed())); err != nil || len(out.got) != 1 || out.got[0].UserId != "u1" {
		t.Errorf("after the database is back: %v, %+v", err, out.got)
	}
}

// captureLog collects what's logged during t.
func captureLog(t *testing.T) *bytes.Buffer {
	var b bytes.Buffer
	log.SetOutput(&b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &b
}
