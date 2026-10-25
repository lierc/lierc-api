package stream

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/lierc/lierc-api/internal/ids"
	"github.com/nsqio/go-nsq"
)

// Consume subscribes to topic "logged" on a channel of its own, so every
// stream process sees every message, and delivers each message until ctx is
// done. If nsqd is down, at start or later, it retries every NSQReconnect;
// messages published meanwhile are lost. It returns early only if the
// consumer cannot be created.
func (s *Server) Consume(ctx context.Context, nsqAddr string) error {
	channel := "lierc-api-go-" + ids.New() + "#ephemeral"
	cfg := nsq.NewConfig()
	// Without nsqlookupd, go-nsq waits LookupdPollInterval (default 60s)
	// before each attempt to reconnect to a lost nsqd.
	cfg.LookupdPollInterval = s.NSQReconnect
	c, err := nsq.NewConsumer("logged", channel, cfg)
	if err != nil {
		return err
	}
	c.SetLogger(log.New(log.Writer(), "nsq: ", log.LstdFlags), nsq.LogLevelWarning)
	c.AddHandler(nsq.HandlerFunc(func(m *nsq.Message) error {
		s.Deliver(m.Body)
		return nil
	}))
	// go-nsq retries only connections that were once up, so the first
	// connection is retried here.
	for {
		err := c.ConnectToNSQD(nsqAddr)
		if err == nil || errors.Is(err, nsq.ErrAlreadyConnected) {
			break
		}
		log.Printf("nsq: connect to %s: %v (retrying in %s)", nsqAddr, err, s.NSQReconnect)
		select {
		case <-ctx.Done():
			c.Stop()
			<-c.StopChan
			return nil
		case <-time.After(s.NSQReconnect):
		}
	}
	<-ctx.Done()
	c.Stop()
	<-c.StopChan
	return nil
}
