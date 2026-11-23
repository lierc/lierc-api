package db

import (
	"context"
	"encoding/json"
	"errors"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
)

// The logger's and its notifier's queries (lierc ingest).

// LogEntry is an IRC line the logger stores.
type LogEntry struct {
	Connection string
	Command    string
	Message    []byte  // the line as JSON
	Time       float64 // when it arrived, in unix seconds
	Self       bool    // sent by the connection's own nick
}

// InsertLog stores e once under each of channels, in order, for its
// connection's owner, and returns the last id. In one transaction, so
// a failure leaves nothing behind for NSQ's retry to duplicate:
//   - a highlight also goes in highlights, with the same id;
//   - a PRIVMSG or NOTICE with a msgid is mapped in message_ids under each
//     channel it was stored in, and a reply goes in replies with its
//     thread's root (mapMessage). Archiving leaves both tables alone.
//
// ErrGone when the connection doesn't exist or is deleted meanwhile:
// nothing was stored. With no channels it stores nothing and returns 0.
func (d *DB) InsertLog(ctx context.Context, e LogEntry, channels []string, highlight bool) (int64, error) {
	if len(channels) == 0 {
		return 0, nil
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// one row per channel, in order, for the connection's owner: none (no
	// row from connections) when the connection doesn't exist
	logged := make([]loggedRow, 0, len(channels))
	for _, channel := range channels {
		var id int64
		err := queryRow(ctx, tx, psql.Insert("logs").
			Columns("user_id", "connection_id", "channel", "command", "message", "time", "self", "highlight").
			Select(psql.Select("user_id", "id").
				Column("?::text", channel).Column("?::text", e.Command).Column("?::jsonb", e.Message).
				Column("to_timestamp(?)", e.Time).Column("?::boolean", e.Self).Column("?::boolean", highlight).
				From("connections").Where(sq.Eq{"id": e.Connection})).
			Suffix("RETURNING id")).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrGone
		}
		if err != nil {
			return 0, gone(err)
		}
		logged = append(logged, loggedRow{ID: id, Channel: channel})
	}

	if highlight {
		ids := make([]int64, len(logged))
		for i, l := range logged {
			ids[i] = l.ID
		}
		cols := []string{"id", "user_id", "connection_id", "channel", "time", "message", "self"}
		if _, err := exec(ctx, tx, psql.Insert("highlights").Columns(cols...).
			Select(psql.Select(cols...).From("logs").Where(sq.Eq{"id": ids}))); err != nil {
			return 0, gone(err)
		}
	}
	if err := mapMessage(ctx, tx, e, logged); err != nil {
		return 0, gone(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, gone(err)
	}
	return logged[len(logged)-1].ID, nil
}

// loggedRow is a logs row InsertLog stored: its id and channel.
type loggedRow struct {
	ID      int64
	Channel string
}

// mapMessage maps a stored PRIVMSG or NOTICE that has a msgid in
// message_ids, under each channel it was stored in (logged), and records
// it in replies if it's a reply (+reply, else +draft/reply), with its
// thread's root. Anything else is left alone.
func mapMessage(ctx context.Context, tx pgx.Tx, e LogEntry, logged []loggedRow) error {
	if e.Command != "PRIVMSG" && e.Command != "NOTICE" {
		return nil
	}
	var m struct{ Tags map[string]string }
	if err := json.Unmarshal(e.Message, &m); err != nil {
		return err
	}
	msgid := m.Tags["msgid"]
	if msgid == "" {
		return nil
	}
	replyTo := m.Tags["+reply"]
	if replyTo == "" {
		replyTo = m.Tags["+draft/reply"]
	}
	var root string
	if replyTo != "" {
		var err error
		if root, err = threadRoot(ctx, tx, e.Connection, replyTo); err != nil {
			return err
		}
	}
	for _, l := range logged {
		channel, err := channelID(ctx, tx, e.Connection, l.Channel)
		if err != nil {
			return err
		}
		if _, err := exec(ctx, tx, psql.Insert("message_ids").Columns("channel_id", "msgid", "log_id").
			Values(channel, msgid, l.ID)); err != nil {
			return err
		}
		if replyTo != "" {
			if _, err := exec(ctx, tx, psql.Insert("replies").Columns("channel_id", "msgid", "reply_to", "thread_root").
				Values(channel, msgid, replyTo, root)); err != nil {
				return err
			}
		}
	}
	return nil
}

// channelID is the channels row of channel on conn, made the first time.
// A row another transaction made meanwhile is found too: ON CONFLICT waits
// for it, and the SELECT after it is a new statement, so it sees it.
func channelID(ctx context.Context, tx pgx.Tx, conn, channel string) (int32, error) {
	var id int32
	err := queryRow(ctx, tx, psql.Insert("channels").Columns("connection_id", "channel").Values(conn, channel).
		Suffix("ON CONFLICT DO NOTHING RETURNING id")).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = queryRow(ctx, tx, psql.Select("id").From("channels").
			Where(sq.Eq{"connection_id": conn, "channel": channel})).Scan(&id)
	}
	return id, err
}

// threadRoot is the root of the thread a reply to replyTo on conn belongs
// to: replyTo's own root when it's a reply on this connection (logged
// before: the logger stores one line at a time), else replyTo itself.
func threadRoot(ctx context.Context, tx pgx.Tx, conn, replyTo string) (string, error) {
	var root string
	err := queryRow(ctx, tx, psql.Select("r.thread_root").From("replies AS r").
		Join("channels AS c ON c.id = r.channel_id").
		Where(sq.Eq{"r.msgid": replyTo, "c.connection_id": conn}).
		OrderBy("r.channel_id").Limit(1)).Scan(&root)
	if errors.Is(err, pgx.ErrNoRows) {
		return replyTo, nil
	}
	return root, err
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
