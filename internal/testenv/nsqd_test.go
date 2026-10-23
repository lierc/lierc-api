package testenv_test

import (
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/testenv"
	"github.com/nsqio/go-nsq"
)

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
