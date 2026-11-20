package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// LISTEN and NOTIFY: highlights (lierc-api tells the logger to reload
// highlight words) and logged (the logger hands each message it handled to
// the stream server).

// ListenerName is the application_name of ListenHighlights' connection;
// LoggedListenerName is ListenLogged's.
const (
	ListenerName       = "lierc highlights listener"
	LoggedListenerName = "lierc logged listener"
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
	d.listenLoop(ctx, "highlights", ListenerName, retry, changed, func(string) { changed() }, report)
}

// NotifyLogged sends payload, a message the logger handled, to ListenLogged.
// It goes to whoever listens when it's sent; nothing keeps it for later.
func (d *DB) NotifyLogged(ctx context.Context, payload []byte) error {
	if len(payload) > MaxNotifyPayload {
		return fmt.Errorf("%w: %d bytes", ErrPayloadTooLong, len(payload))
	}
	_, err := d.Pool.Exec(ctx, `SELECT pg_notify('logged', $1)`, string(payload))
	return err
}

// ListenLogged calls deliver with each NotifyLogged payload. Messages sent
// while it's reconnecting are missed: clients catch up from the database.
// See listenLoop.
func (d *DB) ListenLogged(ctx context.Context, retry time.Duration, deliver func(payload []byte), report func(error)) {
	d.listenLoop(ctx, "logged", LoggedListenerName, retry, func() {},
		func(p string) { deliver([]byte(p)) }, report)
}

// listenLoop listens on channel, on a connection of its own outside the
// pool, named name in pg_stat_activity, calling connected each time it
// starts listening and notified with each payload. It reconnects retry
// apart until ctx is done; each lost or failed connection goes to report.
// The callbacks run on listenLoop's goroutine, so the next notification
// waits for them.
func (d *DB) listenLoop(ctx context.Context, channel, name string, retry time.Duration, connected func(), notified func(payload string), report func(error)) {
	cfg := d.Pool.Config().ConnConfig.Copy()
	cfg.RuntimeParams["application_name"] = name
	for {
		err := listen(ctx, cfg, channel, connected, notified)
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

// listen connects, listens on channel, calls connected, then notified with
// every notification, until the connection fails or ctx is done.
func listen(ctx context.Context, cfg *pgx.ConnConfig, channel string, connected func(), notified func(string)) error {
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return wrap(err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.Close(closeCtx)
	}()
	if _, err := c.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
		return wrap(err)
	}
	connected()

	for {
		wait, cancel := context.WithTimeout(ctx, listenPing)
		n, err := c.WaitForNotification(wait)
		cancel()
		switch {
		case err == nil:
			notified(n.Payload)
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
