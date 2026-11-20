package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	vm "github.com/VictoriaMetrics/metrics"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc/pkg/ircwire"
)

type fakeLoggedNotifier struct {
	payload []byte
	err     error
}

func (n *fakeLoggedNotifier) NotifyLogged(ctx context.Context, payload []byte) error {
	if _, ok := ctx.Deadline(); !ok {
		panic("NotifyLogged without a deadline")
	}
	n.payload = payload
	return n.err
}

// PGOutput sends the message as JSON, the form the stream server reads
// (ConnectionId and UserId strings, MessageId a number).
func TestPGOutput(t *testing.T) {
	n := &fakeLoggedNotifier{}
	PGOutput{DB: n}.Logged(&LoggedMessage{ConnectionId: "c1", MessageId: 7, Highlight: true, UserId: "u1",
		Message: &ircwire.IRCMessage{Command: "PRIVMSG", Params: []string{"#a", "hi"}}})
	var got struct {
		ConnectionId string
		MessageId    int64
		Highlight    bool
		UserId       string
		Message      struct{ Command string }
	}
	if err := json.Unmarshal(n.payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.ConnectionId != "c1" || got.MessageId != 7 || !got.Highlight || got.UserId != "u1" || got.Message.Command != "PRIVMSG" {
		t.Errorf("sent %s", n.payload)
	}
}

// A failed NOTIFY is only logged: the message is already stored, and the
// handler mustn't retry it.
func TestPGOutputFailureLogged(t *testing.T) {
	b := captureLog(t)
	PGOutput{DB: &fakeLoggedNotifier{err: errors.New("database is down")}}.Logged(
		&LoggedMessage{ConnectionId: "c1", MessageId: 3, Message: &ircwire.IRCMessage{Command: "PRIVMSG"}})
	if !strings.Contains(b.String(), "notifying logged (message 3 on c1): database is down") {
		t.Errorf("logged %q", b.String())
	}
}

func TestNSQDAddress(t *testing.T) {
	for host, want := range map[string]string{"127.0.0.1": "127.0.0.1:4150", "nsqd:4160": "nsqd:4160", "::1": "[::1]:4150"} {
		if got := (&Config{NSQDHost: host}).nsqd(); got != want {
			t.Errorf("%q: %q, want %q", host, got, want)
		}
	}
}

// metricValue is a sample's value from the registry's Prometheus text, or
// -1 when it isn't there.
func metricValue(t *testing.T, sample string) float64 {
	t.Helper()
	var b bytes.Buffer
	vm.WritePrometheus(&b, false)
	for _, line := range strings.Split(b.String(), "\n") {
		if name, value, ok := strings.Cut(line, " "); ok && name == sample {
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				t.Fatal(err)
			}
			return f
		}
	}
	return -1
}

// Each payload's size is recorded by kind, the largest is kept, and one
// NOTIFY refused for its size is counted.
func TestPGOutputPayloadMetrics(t *testing.T) {
	count := func(kind string) float64 {
		return metricValue(t, `logger_notify_payload_bytes_count{kind="`+kind+`"}`)
	}
	stored0, passed0 := count("stored"), count("passed")
	tooLong0 := tooLongTotal["passed"].Get()

	n := &fakeLoggedNotifier{}
	o := PGOutput{DB: n}
	o.Logged(&LoggedMessage{ConnectionId: "c1", MessageId: 4, UserId: "u1", Message: &ircwire.IRCMessage{Command: "PRIVMSG"}})
	storedSize := len(n.payload)
	big := &LoggedMessage{ConnectionId: "c1", UserId: "u1", Message: &ircwire.IRCMessage{Command: "TAGMSG",
		Tags: map[string]string{"+x": strings.Repeat("y", 3000)}}}
	o.Logged(big)
	bigSize := len(n.payload)

	if got := count("stored"); got != stored0+1 {
		t.Errorf("stored count %v, want %v", got, stored0+1)
	}
	if got := count("passed"); got != passed0+1 {
		t.Errorf("passed count %v, want %v", got, passed0+1)
	}
	if got := metricValue(t, `logger_notify_payload_max_bytes{kind="passed"}`); got < float64(bigSize) {
		t.Errorf("passed max %v, want at least %d", got, bigSize)
	}
	if got := metricValue(t, `logger_notify_payload_max_bytes{kind="stored"}`); got < float64(storedSize) {
		t.Errorf("stored max %v, want at least %d", got, storedSize)
	}
	if got := metricValue(t, `logger_notify_payload_bytes_bucket{kind="passed",le="4096"}`); got < 1 {
		t.Errorf("passed le=4096 bucket %v, want the big payload in it", got)
	}

	// refused for its size: counted, and logged
	captureLog(t)
	PGOutput{DB: &fakeLoggedNotifier{err: fmt.Errorf("%w: 9000 bytes", db.ErrPayloadTooLong)}}.Logged(big)
	if got := tooLongTotal["passed"].Get(); got != tooLong0+1 {
		t.Errorf("too long %d, want %d", got, tooLong0+1)
	}
}
