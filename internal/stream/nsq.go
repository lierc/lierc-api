package stream

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/lierc/lierc-api/internal/ids"
	"github.com/nsqio/go-nsq"
)

// Consume subscribes to topic "logged", whose messages it delivers, and
// topic "status", liercd's connection state, which it sends as
// connection:connect and connection:disconnect. Each is on a channel of its
// own, so every stream process sees every message, until ctx is done. If
// nsqd is down, at start or later, it retries every NSQReconnect; messages
// published meanwhile are lost. It returns early only if a consumer cannot
// be created.
func (s *Server) Consume(ctx context.Context, nsqAddr string) error {
	status, err := s.consumer("status", func(b []byte) { s.statusChanged(b) })
	if err != nil {
		return err
	}
	logged, err := s.consumer("logged", s.Deliver)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	go func() { s.connect(ctx, status, nsqAddr); close(done) }()
	s.connect(ctx, logged, nsqAddr)
	<-done
	return nil
}

func (s *Server) consumer(topic string, handle func([]byte)) (*nsq.Consumer, error) {
	channel := "lierc-api-go-" + ids.New() + "#ephemeral"
	cfg := nsq.NewConfig()
	// Without nsqlookupd, go-nsq waits LookupdPollInterval (default 60s)
	// before each attempt to reconnect to a lost nsqd.
	cfg.LookupdPollInterval = s.NSQReconnect
	c, err := nsq.NewConsumer(topic, channel, cfg)
	if err != nil {
		return nil, err
	}
	c.SetLogger(log.New(log.Writer(), "nsq: ", log.LstdFlags), nsq.LogLevelWarning)
	c.AddHandler(nsq.HandlerFunc(func(m *nsq.Message) error {
		handle(m.Body)
		return nil
	}))
	return c, nil
}

// connect connects c to nsqd, retrying the first connection (go-nsq retries
// only connections that were once up), and stops it when ctx is done.
func (s *Server) connect(ctx context.Context, c *nsq.Consumer, nsqAddr string) {
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
			return
		case <-time.After(s.NSQReconnect):
		}
	}
	<-ctx.Done()
	c.Stop()
	<-c.StopChan
}
