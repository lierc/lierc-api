package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// LISTEN and NOTIFY: highlights (lierc-api tells the logger to reload
// highlight words) and logged (the logger hands the stream server each
// message it handled).

// ListenerName is the application_name of ListenHighlights' connection;
// StreamListenerName is ListenStream's.
const (
	ListenerName       = "lierc highlights listener"
	StreamListenerName = "lierc stream listener"
)

// MaxNotifyPayload is the longest payload NOTIFY takes, in bytes: Postgres
// refuses 8000 or more.
const MaxNotifyPayload = 7999

// ErrPayloadTooLong is NotifyLogged's error for a payload over
// MaxNotifyPayload; nothing was sent.
var ErrPayloadTooLong = errors.New("payload too long for NOTIFY")

// listenPing is how long a listener waits for a notification before
// pinging its connection, so one that died quietly is noticed.
var listenPing = 90 * time.Second

// ListenHighlights calls changed whenever NotifyHighlights is called, and
// each time it starts listening, at first and after a lost connection,
// since a NOTIFY sent meanwhile was missed. See listenLoop.
func (d *DB) ListenHighlights(ctx context.Context, retry time.Duration, changed func(), report func(error)) {
	d.listenLoop(ctx, ListenerName, retry, changed,
		map[string]func(string){"highlights": func(string) { changed() }}, report)
}

// NotifyLogged sends payload, a message the logger handled, to ListenStream.
// It goes to whoever listens when it's sent; nothing keeps it for later.
func (d *DB) NotifyLogged(ctx context.Context, payload []byte) error {
	return d.notify(ctx, "logged", payload)
}

func (d *DB) notify(ctx context.Context, channel string, payload []byte) error {
	if len(payload) > MaxNotifyPayload {
		return fmt.Errorf("%w: %d bytes", ErrPayloadTooLong, len(payload))
	}
	_, err := d.Pool.Exec(ctx, `SELECT pg_notify($1, $2)`, channel, string(payload))
	return err
}

// ListenStream calls logged with each NotifyLogged payload, in the order
// they were sent. Notifications sent while it's reconnecting are missed:
// clients catch up from the database. See listenLoop.
func (d *DB) ListenStream(ctx context.Context, retry time.Duration, logged func(payload []byte), report func(error)) {
	d.listenLoop(ctx, StreamListenerName, retry, func() {}, map[string]func(string){
		"logged": func(p string) { logged([]byte(p)) },
	}, report)
}

// listenLoop listens on each channel of handlers, on a connection of its
// own outside the pool, named name in pg_stat_activity, calling connected
// each time it starts listening and a channel's handler with each of its
// payloads. It reconnects retry apart until ctx is done; each lost or
// failed connection goes to report. The callbacks run on listenLoop's
// goroutine, so the next notification waits for them.
func (d *DB) listenLoop(ctx context.Context, name string, retry time.Duration, connected func(), handlers map[string]func(payload string), report func(error)) {
	cfg := d.Pool.Config().ConnConfig.Copy()
	cfg.RuntimeParams["application_name"] = name
	for {
		err := listen(ctx, cfg, handlers, connected)
		if ctx.Err() != nil {
			return
		}
		report(err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

// listen connects, listens on each channel of handlers, calls connected,
// then the handler of each notification's channel, until the connection
// fails or ctx is done.
func listen(ctx context.Context, cfg *pgx.ConnConfig, handlers map[string]func(string), connected func()) error {
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return wrap(err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.Close(closeCtx)
	}()
	for channel := range handlers {
		if _, err := c.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
			return wrap(err)
		}
	}
	connected()

	for {
		wait, cancel := context.WithTimeout(ctx, listenPing)
		n, err := c.WaitForNotification(wait)
		cancel()
		switch {
		case err == nil:
			if h := handlers[n.Channel]; h != nil {
				h(n.Payload)
			}
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, context.DeadlineExceeded) && !c.IsClosed():
			if err := c.Ping(ctx); err != nil {
				return wrap(err)
			}
		default:
			return wrap(err)
		}
	}
}
