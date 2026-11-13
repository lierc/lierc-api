package stream

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/lierc/lierc-api/internal/ids"
	"github.com/nsqio/go-nsq"
)

// Consume subscribes to topic "logged", whose messages it delivers, and
// topic "status", liercd's connection state, which it sends as
// connection:connect and connection:disconnect. Each is on a channel of its
// own, so every stream process sees every message, until ctx is done; if
// nsqd is down, at start or later, it retries every NSQReconnect, and
// messages published meanwhile are lost. It also consumes topic "config",
// liercd's own config changes, on the durable configChannel, so those
// queue while the API is away and are saved one at a time, in order. It
// returns early only if a consumer cannot be created.
func (s *Server) Consume(ctx context.Context, nsqAddr string) error {
	status, err := s.consumer("status", func(b []byte) { s.statusChanged(b) })
	if err != nil {
		return err
	}
	logged, err := s.consumer("logged", s.Deliver)
	if err != nil {
		return err
	}
	config, err := s.consumerOn("config", configChannel, s.configChanged)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, c := range []*nsq.Consumer{status, logged, config} {
		wg.Add(1)
		go func() { defer wg.Done(); s.connect(ctx, c, nsqAddr) }()
	}
	wg.Wait()
	return nil
}

// consumer is a consumer of topic on an ephemeral channel of its own,
// whose messages are never retried.
func (s *Server) consumer(topic string, handle func([]byte)) (*nsq.Consumer, error) {
	return s.consumerOn(topic, "lierc-api-go-"+ids.New()+"#ephemeral", func(b []byte) error {
		handle(b)
		return nil
	})
}

// consumerOn is a consumer of topic on channel, one message at a time. A
// message handle fails is delivered again, NSQReconnect times the
// attempts later, for as long as it takes (go-nsq would otherwise drop it
// after 5 attempts); meanwhile the consumer backs off from NSQReconnect
// doubling, up to go-nsq's 2 minutes.
func (s *Server) consumerOn(topic, channel string, handle func([]byte) error) (*nsq.Consumer, error) {
	cfg := nsq.NewConfig()
	// Without nsqlookupd, go-nsq waits LookupdPollInterval (default 60s)
	// before each attempt to reconnect to a lost nsqd.
	cfg.LookupdPollInterval = s.NSQReconnect
	cfg.DefaultRequeueDelay = s.NSQReconnect
	cfg.BackoffMultiplier = s.NSQReconnect
	cfg.MaxAttempts = 0
	c, err := nsq.NewConsumer(topic, channel, cfg)
	if err != nil {
		return nil, err
	}
	c.SetLogger(log.New(log.Writer(), "nsq: ", log.LstdFlags), nsq.LogLevelWarning)
	c.AddHandler(nsq.HandlerFunc(func(m *nsq.Message) error {
		return handle(m.Body)
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
