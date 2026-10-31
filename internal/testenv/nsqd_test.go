package testenv_test

import (
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/testenv"
	"github.com/nsqio/go-nsq"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func TestNSQDRoundTrip(t *testing.T) {
	addr, publish := testenv.NSQD(t)
	cfg := nsq.NewConfig()
	cons, err := nsq.NewConsumer("logged", "test#ephemeral", cfg)
	if err != nil {
		t.Fatal(err)
	}
	cons.SetLogger(nil, nsq.LogLevelError)
	got := make(chan string, 1)
	cons.AddHandler(nsq.HandlerFunc(func(m *nsq.Message) error {
		got <- string(m.Body)
		return nil
	}))
	if err := cons.ConnectToNSQD(addr); err != nil {
		t.Fatal(err)
	}
	defer cons.Stop()
	publish("logged", []byte("hello"))
	select {
	case b := <-got:
		if b != "hello" {
			t.Fatalf("body = %q", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message")
	}
}

// Stopping nsqd just as an ephemeral channel's last consumer leaves races
// nsqd's background deletion of that channel, inside nsqd v1.3.0. With nsqd
// in its own process the race detector never sees it. Repeated, because the
// deletion and the stop have to overlap; run with -race.
func TestNSQDStopAfterEphemeralConsumerLeaves(t *testing.T) {
	for i := 0; i < 100; i++ {
		t.Run("", func(t *testing.T) {
			addr, _, stop := testenv.NSQDAt(t, "127.0.0.1:0")
			cons, err := nsq.NewConsumer("logged", "stop#ephemeral", nsq.NewConfig())
			if err != nil {
				t.Fatal(err)
			}
			cons.SetLogger(nil, nsq.LogLevelError)
			cons.AddHandler(nsq.HandlerFunc(func(*nsq.Message) error { return nil }))
			if err := cons.ConnectToNSQD(addr); err != nil {
				t.Fatal(err)
			}
			cons.Stop()
			stop()
			<-cons.StopChan
		})
	}
}
