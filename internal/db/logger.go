package db

import (
	"context"
	"errors"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
)

// The logger's and its notifier's queries (cmd/lierc-logger).

// LogEntry is an IRC line the logger stores.
type LogEntry struct {
	Connection string
	Command    string
	Message    []byte  // the line as JSON
	Time       float64 // when it arrived, in unix seconds
	Self       bool    // sent by the connection's own nick
}

// InsertLog stores e once under each of channels, in order, for its
// connection's owner, and returns the last id; a highlight also goes in
// highlights, with the same id. ErrGone when the connection doesn't exist
// or is deleted meanwhile: nothing was stored. With no channels it stores
// nothing and returns 0.
func (d *DB) InsertLog(ctx context.Context, e LogEntry, channels []string, highlight bool) (int64, error) {
	if len(channels) == 0 {
		return 0, nil
	}
	var id *int64
	err := d.Pool.QueryRow(ctx, Query("insert_logs"),
		e.Connection, channels, e.Command, e.Message, e.Time, e.Self, highlight).Scan(&id)
	if err != nil {
		return 0, gone(err)
	}
	if id == nil {
		return 0, ErrGone
	}
	return *id, nil
}

// SavePrivateAt records a private message with nick on conn at unix time
// at, unlike AddPrivate's now(). Nothing is stored for a connection that
// doesn't exist; ErrGone when it's deleted meanwhile.
func (d *DB) SavePrivateAt(ctx context.Context, conn, nick string, at float64) error {
	_, err := exec(ctx, d.Pool, psql.Insert("private_chats").Columns("connection_id", "nick", "time").
		Select(sub.Select("id").Column("?::text", nick).Column("to_timestamp(?)", at).
			From("connections").Where(sq.Eq{"id": conn})).
		Suffix(`ON CONFLICT (connection_id, nick) DO UPDATE SET time = EXCLUDED.time`))
	return gone(err)
}

// HighlightWords is each connection's highlight words, the JSON of its
// config's Highlight; connections without any are left out.
func (d *DB) HighlightWords(ctx context.Context) (map[string]string, error) {
	rows, err := query(ctx, d.Pool, psql.Select("id", "config->>'Highlight'").From("connections"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	words := make(map[string]string)
	for rows.Next() {
		var id string
		var w *string
		if err := rows.Scan(&id, &w); err != nil {
			return nil, err
		}
		if w != nil {
			words[id] = *w
		}
	}
	return words, rows.Err()
}

// NotifyTarget is who a connection's highlights go to, and how.
type NotifyTarget struct {
	User  string
	Email string
	// EmailOn is whether to email: the user's email pref is "true" and
	// the address is confirmed, or from before email confirmation
	// (verified NULL). Signups can chat before confirming, and an
	// unconfirmed address may be someone else's.
	EmailOn bool
	WebPush []WebPushSubscription
}

// WebPushSubscription is one of a user's push subscriptions.
type WebPushSubscription struct {
	Endpoint, Key, Auth string
}

// NotifyTarget looks up conn's owner and their notification settings; ok
// is false when there's no such connection.
func (d *DB) NotifyTarget(ctx context.Context, conn string) (t NotifyTarget, ok bool, err error) {
	var pref *string
	var confirmed bool
	err = queryRow(ctx, d.Pool, psql.Select("u.id", "u.email", "p.value", "coalesce(u.verified, true)").
		From("connections AS c").
		Join("users AS u ON u.id = c.user_id").
		LeftJoin("prefs AS p ON p.user_id = u.id AND p.name = 'email'").
		Where(sq.Eq{"c.id": conn})).Scan(&t.User, &t.Email, &pref, &confirmed)
	if errors.Is(err, pgx.ErrNoRows) {
		return NotifyTarget{}, false, nil
	}
	if err != nil {
		return NotifyTarget{}, false, err
	}
	t.EmailOn = confirmed && pref != nil && *pref == "true"

	rows, err := query(ctx, d.Pool, psql.Select("endpoint", "key", "auth").
		From("web_push_subscriptions").Where(sq.Eq{"user_id": t.User}).OrderBy("endpoint"))
	if err != nil {
		return NotifyTarget{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var s WebPushSubscription
		if err := rows.Scan(&s.Endpoint, &s.Key, &s.Auth); err != nil {
			return NotifyTarget{}, false, err
		}
		t.WebPush = append(t.WebPush, s)
	}
	if err := rows.Err(); err != nil {
		return NotifyTarget{}, false, err
	}
	return t, true, nil
}

// ListenerName is the application_name of ListenHighlights' connection.
const ListenerName = "lierc highlights listener"

// listenPing is how long ListenHighlights waits for a notification before
// pinging its connection, so one that died quietly is noticed.
var listenPing = 90 * time.Second

// ListenHighlights calls changed whenever NotifyHighlights is called, and
// each time it starts listening, at first and after a lost connection,
// since a NOTIFY sent meanwhile was missed. It listens on a connection of
// its own, outside the pool, reconnecting retry apart, until ctx is done;
// each lost or failed connection goes to report. changed runs on
// ListenHighlights' goroutine, so the next NOTIFY waits for it.
func (d *DB) ListenHighlights(ctx context.Context, retry time.Duration, changed func(), report func(error)) {
	cfg := d.Pool.Config().ConnConfig.Copy()
	// named, so pg_stat_activity tells it from the pool's
	cfg.RuntimeParams["application_name"] = ListenerName
	for {
		err := listen(ctx, cfg, "highlights", changed)
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

// listen connects, listens on channel and calls changed then and on every
// notification, until the connection fails or ctx is done.
func listen(ctx context.Context, cfg *pgx.ConnConfig, channel string, changed func()) error {
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
	changed()

	for {
		wait, cancel := context.WithTimeout(ctx, listenPing)
		_, err := c.WaitForNotification(wait)
		cancel()
		switch {
		case err == nil:
			changed()
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
