package stream

import (
	"context"
	"log"

	"github.com/lierc/lierc-api/internal/ids"
	"github.com/nsqio/go-nsq"
)

// Consume subscribes to topic "logged" on a channel of its own, so every
// stream process sees every message, and delivers each message until ctx is
// done. It returns early only if it cannot connect.
func (s *Server) Consume(ctx context.Context, nsqAddr string) error {
	channel := "lierc-api-go-" + ids.New() + "#ephemeral"
	c, err := nsq.NewConsumer("logged", channel, nsq.NewConfig())
	if err != nil {
		return err
	}
	c.SetLogger(log.New(log.Writer(), "nsq: ", log.LstdFlags), nsq.LogLevelWarning)
	c.AddHandler(nsq.HandlerFunc(func(m *nsq.Message) error {
		s.Deliver(m.Body)
		return nil
	}))
	if err := c.ConnectToNSQD(nsqAddr); err != nil {
		c.Stop()
		<-c.StopChan
		return err
	}
	<-ctx.Done()
	c.Stop()
	<-c.StopChan
	return nil
}
