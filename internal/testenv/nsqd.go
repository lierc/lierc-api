package testenv

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nsqio/nsq/nsqd"
)

type quietLogger struct{}

func (quietLogger) Output(int, string) error { return nil }

// NSQD starts an in-process nsqd on a random port and returns its TCP address
// and a function that publishes a message to a topic.
func NSQD(t testing.TB) (tcpAddr string, publish func(topic string, body []byte)) {
	t.Helper()
	tcpAddr, publish, _ = NSQDAt(t, "127.0.0.1:0")
	return tcpAddr, publish
}

// NSQDAt is NSQD listening on addr, and also returns a function that shuts
// the nsqd down (it is shut down at cleanup either way).
func NSQDAt(t testing.TB, addr string) (tcpAddr string, publish func(topic string, body []byte), stop func()) {
	t.Helper()
	opts := nsqd.NewOptions()
	opts.TCPAddress = addr
	opts.HTTPAddress = ""
	opts.HTTPSAddress = ""
	opts.BroadcastAddress = "127.0.0.1"
	opts.DataPath = t.TempDir()
	opts.Logger = quietLogger{}
	n, err := nsqd.New(opts)
	if err != nil {
		t.Fatalf("nsqd.New: %v", err)
	}
	go n.Main()
	var once sync.Once
	stop = func() { once.Do(n.Exit) }
	t.Cleanup(stop)
	var seq atomic.Uint64
	// IDs must be hex text like nsqd's own: clients echo them in
	// newline-terminated FIN commands, so raw bytes such as '\n' break the
	// connection.
	id := func() (m nsqd.MessageID) {
		copy(m[:], fmt.Sprintf("%016x", seq.Add(1)))
		return m
	}
	return n.RealTCPAddr().String(), func(topic string, body []byte) {
		msg := nsqd.NewMessage(id(), body)
		if err := n.GetTopic(topic).PutMessage(msg); err != nil {
			t.Errorf("nsqd publish: %v", err)
		}
	}, stop
}
