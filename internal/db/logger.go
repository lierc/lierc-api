package db

import (
	"context"
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
