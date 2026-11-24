package db

import (
	"context"
	"errors"

	sq "github.com/Masterminds/squirrel"
)

// The small writes.

// ErrGone is a write whose user or connection was deleted after the caller
// checked it (the row fails its key, 23503). Nothing was written.
var ErrGone = errors.New("its user or connection is gone")

// gone turns a key violation into ErrGone.
func gone(err error) error {
	if isCode(err, "23503") {
		return ErrGone
	}
	return err
}

// SetPref saves user's preference name; ErrGone if user is gone.
func (d *DB) SetPref(ctx context.Context, user, name, value string) error {
	_, err := exec(ctx, d.Pool, psql.Insert("prefs").Columns("user_id", "name", "value").Values(user, name, value).
		Suffix(`ON CONFLICT (user_id, name) DO UPDATE SET value=EXCLUDED.value`))
	return gone(err)
}

// SetSeen records the last message user has seen in channel on conn. It
// writes only on the conversation's channels row, of a connection user
// owns: a seen marker names a logged message, whose line made the row, so
// one for a conversation with none is ignored. ErrGone, as before, when
// conn is gone or isn't user's (deleted after the caller checked it).
func (d *DB) SetSeen(ctx context.Context, user, conn, channel string, id int32) error {
	n, err := exec(ctx, d.Pool, psql.Insert("seen_markers").
		Columns("channel_id", "message_id").
		Select(sub.Select("ch.id").Column("?::integer", id).
			From("channels AS ch").Join("connections AS c ON c.id = ch.connection_id").
			Where(sq.Eq{"ch.connection_id": conn, "ch.channel": channel, "c.user_id": user})).
		Suffix(`ON CONFLICT (channel_id) DO UPDATE SET message_id=EXCLUDED.message_id, updated=now()`))
	if err != nil || n.RowsAffected() > 0 {
		return gone(err)
	}
	var owned bool
	if err := queryRow(ctx, d.Pool, psql.Select().Column(sq.Expr("EXISTS (?)",
		sub.Select("1").From("connections").Where(sq.Eq{"id": conn, "user_id": user})))).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return ErrGone
	}
	return nil
}

// AddPrivate opens, or touches, a private chat with nick on conn; ErrGone
// if conn is gone.
func (d *DB) AddPrivate(ctx context.Context, conn, nick string) error {
	_, err := exec(ctx, d.Pool, psql.Insert("private_chats").Columns("connection_id", "nick", "time").
		Values(conn, nick, sq.Expr("now()")).
		Suffix(`ON CONFLICT (connection_id, nick) DO UPDATE SET time=now()`))
	return gone(err)
}

// DeletePrivate closes the private chat with nick on conn.
func (d *DB) DeletePrivate(ctx context.Context, conn, nick string) error {
	_, err := exec(ctx, d.Pool, psql.Delete("private_chats").Where(sq.Eq{"connection_id": conn, "nick": nick}))
	return err
}

// SaveWebPush saves user's push subscription; ErrGone if user is gone.
func (d *DB) SaveWebPush(ctx context.Context, user, endpoint, key, auth string) error {
	_, err := exec(ctx, d.Pool, psql.Insert("web_push_subscriptions").Columns("endpoint", "auth", "key", "user_id").
		Values(endpoint, auth, key, user).
		Suffix(`ON CONFLICT (user_id, endpoint) DO UPDATE SET auth=EXCLUDED.auth, key=EXCLUDED.key`))
	return gone(err)
}

// DeleteWebPush forgets user's subscription at endpoint.
func (d *DB) DeleteWebPush(ctx context.Context, user, endpoint string) error {
	_, err := exec(ctx, d.Pool, psql.Delete("web_push_subscriptions").Where(sq.Eq{"user_id": user, "endpoint": endpoint}))
	return err
}
