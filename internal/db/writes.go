package db

import "context"

// The small writes, ported from lib/API/Controller/*.pm.

// SetPref saves user's preference name (Pref.pm upsert).
func (d *DB) SetPref(ctx context.Context, user, name, value string) error {
	_, err := d.Pool.Exec(ctx, `
		INSERT INTO pref ("user", name, value) VALUES ($1, $2, $3)
		ON CONFLICT ("user", name) DO UPDATE SET value=EXCLUDED.value`, user, name, value)
	return err
}

// SetSeen records the last message user has seen in channel on conn
// (Channel.pm set_seen).
func (d *DB) SetSeen(ctx context.Context, user, conn, channel string, id int32) error {
	_, err := d.Pool.Exec(ctx, `
		INSERT INTO seen ("user", connection, channel, message_id) VALUES ($1, $2, $3, $4)
		ON CONFLICT ("user", connection, channel)
		DO UPDATE SET message_id=EXCLUDED.message_id, updated=NOW()`, user, conn, channel, id)
	return err
}

// AddPrivate opens, or touches, a private chat with nick on conn
// (Private.pm create).
func (d *DB) AddPrivate(ctx context.Context, conn, nick string) error {
	_, err := d.Pool.Exec(ctx, `
		INSERT INTO private (connection, nick, time) VALUES ($1, $2, NOW())
		ON CONFLICT (connection, nick) DO UPDATE SET time=NOW()`, conn, nick)
	return err
}

// DeletePrivate closes the private chat with nick on conn (Private.pm
// delete).
func (d *DB) DeletePrivate(ctx context.Context, conn, nick string) error {
	_, err := d.Pool.Exec(ctx, `DELETE FROM private WHERE connection=$1 AND nick=$2`, conn, nick)
	return err
}

// SaveWebPush saves user's push subscription (WebPush.pm upsert).
func (d *DB) SaveWebPush(ctx context.Context, user, endpoint, key, auth string) error {
	_, err := d.Pool.Exec(ctx, `
		INSERT INTO web_push (endpoint, auth, key, "user") VALUES ($1, $2, $3, $4)
		ON CONFLICT ("user", endpoint) DO UPDATE SET auth=EXCLUDED.auth, key=EXCLUDED.key`,
		endpoint, auth, key, user)
	return err
}

// DeleteWebPush forgets user's subscription at endpoint (WebPush.pm
// delete).
func (d *DB) DeleteWebPush(ctx context.Context, user, endpoint string) error {
	_, err := d.Pool.Exec(ctx, `DELETE FROM web_push WHERE "user"=$1 AND endpoint=$2`, user, endpoint)
	return err
}
