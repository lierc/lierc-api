// Package logservice runs the logger and its notifier together: what
// cmd/lierc-logger runs, and what another process (lierc-api) could run
// in place of it, handing the logger's output and the notifier's stream
// counts to its own stream server.
package logservice

import (
	"context"
	"log"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/logger"
	"github.com/lierc/lierc-api/internal/notify"
)

// Options are what a process running the service may replace.
type Options struct {
	// Output gets every message the logger handles; nil means a Postgres
	// NOTIFY on "logged" (logger.PGOutput), which the stream server
	// listens on.
	Output logger.Output
	// Streams tells the notifier who's online; nil means asking
	// API_STATS over HTTP.
	Streams notify.StreamCounter
}

// Service is the logger and its notifier (nil when notifications are
// off).
type Service struct {
	logger   *logger.Logger
	notifier *notify.Notifier
	// accumulating: Start started the notifier, so Stop waits for it
	accumulating bool
}

// New builds the service from the environment, on d, which stays the
// caller's to close after Stop.
func New(getenv func(string) string, d *db.DB, o Options) (*Service, error) {
	conf := &logger.Config{NSQDHost: getenv("NSQD_HOST")}
	s := &Service{}
	out := o.Output
	if out == nil {
		out = logger.PGOutput{DB: d}
	}
	s.logger = logger.New(conf, d, out)

	// Only a non-nil notifier is set: a nil *notify.Notifier would make a
	// non-nil logger.Notifier.
	if n := newNotifier(getenv, d); n != nil {
		n.Streams = o.Streams
		s.notifier = n
		s.logger.SetNotifier(n)
	}
	return s, nil
}

// Start starts the notifier, then the logger, which waits for postgres
// and nsqd; see logger.Start. On an error, nothing is left running but
// the notifier, which Stop stops.
func (s *Service) Start(ctx context.Context) error {
	if s.notifier != nil {
		s.accumulating = true
		go s.notifier.Accumulate()
	}
	return s.logger.Start(ctx)
}

// Stop stops the logger, once the messages in flight are handled, then the
// notifier. It may follow a Start that failed, or none.
func (s *Service) Stop() {
	s.logger.Stop()
	if s.accumulating {
		s.notifier.Stop()
	}
}

// newNotifier builds the notifier from the environment, sharing store. A
// setting that can't be parsed turns notifications off (nil) rather than
// stopping the logger.
func newNotifier(getenv func(string) string, store notify.Store) *notify.Notifier {
	conf, err := notify.ConfigFromEnv(getenv)
	if err != nil {
		log.Printf("notifications off: %v", err)
		return nil
	}
	return notify.New(conf, store)
}
