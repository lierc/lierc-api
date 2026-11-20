// Package logger stores IRC messages from liercd in the log table and
// hands them, with their ids, to its Output for the API's event streams
// (NSQOutput: the NSQ topic "logged"). It hands highlights and private
// messages to the notifier, which runs in the same process.
//
// It reads the NSQ topic "chats", written by liercd: lines from a
// connection, where NICK and QUIT carry the channels they belong in.
package logger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc/pkg/ircwire"
	"github.com/nsqio/go-nsq"
)

type Config struct {
	// NSQDHost is nsqd's host (NSQD_HOST), with or without a port, as
	// lierc-api reads it; without one, nsqd's default 4150 is used.
	NSQDHost string
}

func (c *Config) nsqd() string {
	if _, _, err := net.SplitHostPort(c.NSQDHost); err == nil {
		return c.NSQDHost
	}
	return net.JoinHostPort(c.NSQDHost, "4150")
}

// LoggedMessage is a message the logger handled, as its Output and the
// notifier get it, and as NSQOutput publishes it.
type LoggedMessage struct {
	Message      *ircwire.IRCMessage
	ConnectionId string
	MessageId    int
	Highlight    bool
	// UserId is the connection's owner, so the stream server needn't
	// look it up. The logger always sets it.
	UserId string
}

// Notifier is sent highlights and private messages once they're stored.
// Notify must not wait: it runs on the NSQ handler that stores messages.
// It may keep e; the logger doesn't change e after handing it over.
type Notifier interface {
	Notify(e *LoggedMessage)
}

// Output gets every message the logger handles, once stored. Logged must
// not wait for long: it runs on the NSQ handler that stores messages.
// Failures are its own to report; the message is already stored, and
// retrying it would store it twice. It may keep e, which the logger
// doesn't change after handing it over.
type Output interface {
	Logged(e *LoggedMessage)
}

type Logger struct {
	config     *Config
	db         *db.DB
	store      Store
	out        Output
	consumers  []*nsq.Consumer
	highlights *Highlighters
	owners     owners
	// stopListening stops listenForHighlights' goroutine; listening is
	// closed when it has
	stopListening context.CancelFunc
	listening     chan struct{}
	// notifier is nil when notifications are off
	notifier Notifier
}

// New returns a logger that stores messages in d and hands them to out.
// d and out stay the caller's to close, after Stop.
func New(config *Config, d *db.DB, out Output) *Logger {
	return &Logger{
		config:     config,
		db:         d,
		store:      &pgStore{db: d},
		out:        out,
		highlights: &Highlighters{},
	}
}

// Start loads highlights, starts listening for changes to them, and
// starts consuming the chats topic. It waits for postgres
// and nsqd to be reachable, so it can start before they do.
func (l *Logger) Start(ctx context.Context) error {
	err := retry(ctx, "connecting to postgres", func() error {
		return l.db.Pool.Ping(ctx)
	})
	if err != nil {
		return err
	}

	if err := l.loadHighlights(ctx); err != nil {
		return err
	}
	l.listenForHighlights(ctx)

	for topic, handler := range map[string]nsq.HandlerFunc{
		"chats": l.handleChat,
	} {
		consumer, err := nsq.NewConsumer(topic, "logger", consumerConfig())
		if err != nil {
			return err
		}
		consumer.AddHandler(handler)

		// go-nsq only retries connections that drop, not ones that
		// never connected
		err = retry(ctx, "consuming "+topic, func() error {
			return consumer.ConnectToNSQD(l.config.nsqd())
		})
		if err != nil {
			return err
		}
		l.consumers = append(l.consumers, consumer)
	}

	return nil
}

// Without nsqlookupd, go-nsq waits LookupdPollInterval (60s by default)
// between attempts to reconnect to nsqd, so after nsqd restarts nothing
// was logged for a minute. Retry much sooner.
const reconnectInterval = 5 * time.Second

func consumerConfig() *nsq.Config {
	config := nsq.NewConfig()
	config.LookupdPollInterval = reconnectInterval
	return config
}

// retryInterval is how long retry waits between attempts.
var retryInterval = reconnectInterval

// retry calls f until it succeeds, retryInterval apart, or until ctx is
// done.
func retry(ctx context.Context, what string, f func() error) error {
	for {
		err := f()
		if err == nil {
			return nil
		}
		log.Printf("%s: %v (retrying in %s)", what, err, retryInterval)

		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", what, err)
		case <-time.After(retryInterval):
		}
	}
}

// SetNotifier sets where highlights and private messages go. Call it
// before Start; without it, notifications are off.
func (l *Logger) SetNotifier(n Notifier) {
	l.notifier = n
}

// Stop finishes the messages in flight, then disconnects. It leaves the
// database pool open.
func (l *Logger) Stop() {
	for _, consumer := range l.consumers {
		consumer.Stop()
	}
	for _, consumer := range l.consumers {
		<-consumer.StopChan
	}
	if l.stopListening != nil {
		l.stopListening()
		<-l.listening
	}
}

// handleChat stores a chats message, if it's a kind that's stored, under
// each of its channels (one, but for NICK and QUIT, which come with
// theirs), and publishes it once, with the last id and its connection's
// owner. A message for a connection that doesn't exist, or is deleted
// while it's stored, is dropped silently: liercd can send a few after a
// connection is deleted. When the owner can't be looked up, a message to
// store is returned as an error, for NSQ to retry, as when its insert
// fails; one that's only passed on is dropped, since it's of use only
// live.
func (l *Logger) handleChat(n *nsq.Message) error {
	var m ircwire.IRCClientMessage
	if err := json.Unmarshal(n.Body, &m); err != nil {
		log.Printf("dropping unreadable chats message: %v\n%s", err, n.Body)
		return nil
	}
	if m.Message == nil {
		log.Printf("dropping chats message without a message\n%s", n.Body)
		return nil
	}

	ctx := context.Background()

	route := routeChat(m.Message, m.Channels)
	countCommand(m.Message.Command)
	if route.kind == logSkip {
		return nil
	}

	user, ok, err := l.owner(m.Id)
	if err != nil {
		return l.ownerFailed(&m, route.kind != logPass, err)
	}
	if !ok {
		return nil
	}

	if route.kind == logPass {
		l.publish(&LoggedMessage{Message: m.Message, ConnectionId: m.Id, UserId: user})
		return nil
	}

	if route.private {
		if err := l.store.InsertPrivate(ctx, m.Id, route.channels[0], m.Message.Time); err != nil {
			return fmt.Errorf("logging private message: %w", err)
		}
	}

	highlight := l.highlights.Match(m.Id, m.Message)

	id, err := l.store.InsertMessage(ctx, m.Id, m.Message, route.channels, highlight)
	if errors.Is(err, ErrNoConnection) {
		l.forget(m.Id)
		return nil
	}
	if err != nil {
		return fmt.Errorf("logging message for %s: %w", m.Id, err)
	}
	storedTotal.Inc()
	if highlight {
		highlightsTotal.Inc()
	}

	l.publish(&LoggedMessage{
		Message:      m.Message,
		ConnectionId: m.Id,
		MessageId:    id,
		Highlight:    highlight,
		UserId:       user,
	})
	return nil
}

// ownerFailed handles a failed owner lookup: a message to store goes back
// to NSQ to retry; one that's only passed on is dropped.
func (l *Logger) ownerFailed(m *ircwire.IRCClientMessage, stored bool, err error) error {
	if stored {
		return fmt.Errorf("looking up the owner of %s: %w", m.Id, err)
	}
	log.Printf("dropping %s on %s: looking up its owner: %v", m.Message.Command, m.Id, err)
	return nil
}

// publish hands e to the Output, and to the notifier if it's a highlight
// or a private message.
func (l *Logger) publish(e *LoggedMessage) {
	l.out.Logged(e)
	if l.notifier != nil && (e.Highlight || (e.Message.Direct && !e.Message.Prefix.Self)) {
		l.notifier.Notify(e)
	}
}

// loadHighlights loads the highlights at start, retrying until it can:
// starting with none would silently stop highlights and their
// notifications until the next NOTIFY highlights (as when lierc-api
// hasn't finished migrating the schema yet).
func (l *Logger) loadHighlights(ctx context.Context) error {
	return retry(ctx, "loading highlights", func() error { return l.readHighlights(ctx) })
}

// reloadHighlights reloads the highlights after a NOTIFY highlights; on
// failure the ones already loaded stay.
func (l *Logger) reloadHighlights(ctx context.Context) {
	if err := l.readHighlights(ctx); err != nil {
		log.Printf("loading highlights: %v", err)
	}
}

func (l *Logger) readHighlights(ctx context.Context) error {
	configs, err := l.store.Highlights(ctx)
	if err != nil {
		return err
	}

	patterns, errs := compileHighlights(configs)
	for _, err := range errs {
		log.Print(err)
	}
	l.highlights.Replace(patterns)

	summary := fmt.Sprintf("loaded highlights for %d of %d connections", len(patterns), len(configs))
	if len(errs) > 0 {
		summary += fmt.Sprintf(", %d couldn't be read", len(errs))
	}
	log.Print(summary)
	return nil
}

// listenForHighlights reloads the highlights whenever the API sends
// NOTIFY highlights, and on each (re)connect of its listening connection,
// in case one was missed, until Stop.
func (l *Logger) listenForHighlights(ctx context.Context) {
	ctx, l.stopListening = context.WithCancel(ctx)
	l.listening = make(chan struct{})
	go func() {
		defer close(l.listening)
		l.db.ListenHighlights(ctx, reconnectInterval,
			func() { l.reloadHighlights(ctx) },
			func(err error) { log.Printf("highlight listener: %v (retrying in %s)", err, reconnectInterval) })
	}()
}
