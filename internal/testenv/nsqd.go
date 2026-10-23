package testenv

import (
	"encoding/binary"
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
	opts := nsqd.NewOptions()
	opts.TCPAddress = "127.0.0.1:0"
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
	t.Cleanup(n.Exit)
	var seq atomic.Uint64
	id := func() (m nsqd.MessageID) {
		binary.BigEndian.PutUint64(m[:], seq.Add(1))
		return m
	}
	return n.RealTCPAddr().String(), func(topic string, body []byte) {
		msg := nsqd.NewMessage(id(), body)
		if err := n.GetTopic(topic).PutMessage(msg); err != nil {
			t.Errorf("nsqd publish: %v", err)
		}
	}
}
