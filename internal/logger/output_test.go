package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/lierc/lierc/pkg/ircwire"
)

type fakePublisher struct {
	topic string
	body  []byte
	err   error
}

func (p *fakePublisher) Publish(topic string, body []byte) error {
	p.topic, p.body = topic, body
	return p.err
}

// NSQOutput publishes the message as JSON on "logged", the form the
// stream server reads (ConnectionId a string, MessageId a number).
func TestNSQOutput(t *testing.T) {
	p := &fakePublisher{}
	o := &NSQOutput{pub: p}
	o.Logged(&LoggedMessage{ConnectionId: "c1", MessageId: 7, Highlight: true,
		Message: &ircwire.IRCMessage{Command: "PRIVMSG", Params: []string{"#a", "hi"}}})
	if p.topic != "logged" {
		t.Errorf("topic %q", p.topic)
	}
	var got struct {
		ConnectionId string
		MessageId    int64
		Highlight    bool
		Message      struct{ Command string }
	}
	if err := json.Unmarshal(p.body, &got); err != nil {
		t.Fatal(err)
	}
	if got.ConnectionId != "c1" || got.MessageId != 7 || !got.Highlight || got.Message.Command != "PRIVMSG" {
		t.Errorf("published %s", p.body)
	}
}

// A publish failure is only logged: the message is already stored, and
// the handler mustn't retry it.
func TestNSQOutputFailureLogged(t *testing.T) {
	var b bytes.Buffer
	log.SetOutput(&b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	o := &NSQOutput{pub: &fakePublisher{err: errors.New("nsqd is down")}}
	o.Logged(&LoggedMessage{ConnectionId: "c1", Message: &ircwire.IRCMessage{Command: "PRIVMSG"}})
	if !strings.Contains(b.String(), "publishing to logged: nsqd is down") {
		t.Errorf("logged %q", b.String())
	}
	o.Stop() // no producer: nothing to stop
}

func TestNSQDAddress(t *testing.T) {
	for host, want := range map[string]string{"127.0.0.1": "127.0.0.1:4150", "nsqd:4160": "nsqd:4160", "::1": "[::1]:4150"} {
		if got := (&Config{NSQDHost: host}).nsqd(); got != want {
			t.Errorf("%q: %q, want %q", host, got, want)
		}
	}
}
