package db

import (
	"context"

	sq "github.com/Masterminds/squirrel"
)

// The small writes.

// SetPref saves user's preference name.
func (d *DB) SetPref(ctx context.Context, user, name, value string) error {
	_, err := exec(ctx, d.Pool, psql.Insert("pref").Columns(`"user"`, "name", "value").Values(user, name, value).
		Suffix(`ON CONFLICT ("user", name) DO UPDATE SET value=EXCLUDED.value`))
	return err
}

// SetSeen records the last message user has seen in channel on conn.
func (d *DB) SetSeen(ctx context.Context, user, conn, channel string, id int32) error {
	_, err := exec(ctx, d.Pool, psql.Insert("seen").Columns(`"user"`, "connection", "channel", "message_id").
		Values(user, conn, channel, id).
		Suffix(`ON CONFLICT ("user", connection, channel) DO UPDATE SET message_id=EXCLUDED.message_id, updated=now()`))
	return err
}

// AddPrivate opens, or touches, a private chat with nick on conn.
func (d *DB) AddPrivate(ctx context.Context, conn, nick string) error {
	_, err := exec(ctx, d.Pool, psql.Insert("private").Columns("connection", "nick", "time").
		Values(conn, nick, sq.Expr("now()")).
		Suffix(`ON CONFLICT (connection, nick) DO UPDATE SET time=now()`))
	return err
}

// DeletePrivate closes the private chat with nick on conn.
func (d *DB) DeletePrivate(ctx context.Context, conn, nick string) error {
	_, err := exec(ctx, d.Pool, psql.Delete("private").Where(sq.Eq{"connection": conn, "nick": nick}))
	return err
}

// SaveWebPush saves user's push subscription.
func (d *DB) SaveWebPush(ctx context.Context, user, endpoint, key, auth string) error {
	_, err := exec(ctx, d.Pool, psql.Insert("web_push").Columns("endpoint", "auth", "key", `"user"`).
		Values(endpoint, auth, key, user).
		Suffix(`ON CONFLICT ("user", endpoint) DO UPDATE SET auth=EXCLUDED.auth, key=EXCLUDED.key`))
	return err
}

// DeleteWebPush forgets user's subscription at endpoint.
func (d *DB) DeleteWebPush(ctx context.Context, user, endpoint string) error {
	_, err := exec(ctx, d.Pool, psql.Delete("web_push").Where(sq.Eq{`"user"`: user, "endpoint": endpoint}))
	return err
}
