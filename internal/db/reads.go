package db

import (
	"context"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
)

// Every list here is non-nil when empty, so it encodes as [], not null.

// Pref is one of a user's preferences: its value is the UI's JSON text.
type Pref struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Prefs is user's preferences.
func (d *DB) Prefs(ctx context.Context, user string) ([]Pref, error) {
	rows, err := query(ctx, d.Pool, psql.Select("name", "value").From("prefs").Where(sq.Eq{"user_id": user}))
	if err != nil {
		return nil, err
	}
	return collect(rows, pgx.RowToStructByPos[Pref])
}

// Private is a private chat. Time is the text Postgres prints for the time
// as a UTC timestamp without a zone ("2026-10-29 12:34:56.789"), which the
// UI parses.
type Private struct {
	Nick       string `json:"nick"`
	Connection string `json:"connection"`
	Time       string `json:"time"`
}

// Privates is user's private chats, oldest first.
func (d *DB) Privates(ctx context.Context, user string) ([]Private, error) {
	rows, err := query(ctx, d.Pool, psql.Select("p.nick", "p.connection_id", "(p.time AT TIME ZONE 'UTC')::text").
		From("private_chats AS p").Join("connections AS c ON c.id = p.connection_id").
		Where(sq.Eq{"c.user_id": user}).OrderBy("p.time ASC"))
	if err != nil {
		return nil, err
	}
	return collect(rows, pgx.RowToStructByPos[Private])
}

// SeenRow is the last message a user has seen in a channel.
type SeenRow struct {
	Connection string `json:"connection"`
	Channel    string `json:"channel"`
	MessageID  int64  `json:"message_id"`
}

// Seen is user's seen positions on connections that still exist.
func (d *DB) Seen(ctx context.Context, user string) ([]SeenRow, error) {
	rows, err := query(ctx, d.Pool, psql.Select("ch.connection_id", "ch.channel", "s.message_id::bigint").
		From("seen_markers AS s").Join("channels AS ch ON ch.id = s.channel_id").
		Join("connections AS c ON c.id = ch.connection_id").
		Where(sq.Eq{"c.user_id": user}))
	if err != nil {
		return nil, err
	}
	return collect(rows, pgx.RowToStructByPos[SeenRow])
}

// ChannelNames is the channels conn has logs in, most recently active
// first: its channels rows, each with its newest logs id (one probe of
// logs_channel_id_id_idx).
func (d *DB) ChannelNames(ctx context.Context, conn string) ([]string, error) {
	rows, err := query(ctx, d.Pool, psql.Select("ch.channel").From("channels AS ch").
		JoinClause("CROSS JOIN LATERAL (SELECT max(id) AS newest FROM logs WHERE channel_id = ch.id) l").
		Where(sq.Eq{"ch.connection_id": conn}).Where("l.newest IS NOT NULL").OrderBy("l.newest DESC"))
	if err != nil {
		return nil, err
	}
	return collect(rows, pgx.RowTo[string])
}

// MissedEntry asks what's in Channel on Connection after message After.
type MissedEntry struct {
	Connection, Channel string
	After               int64
}

// MissedCounts is what a channel has had since a MissedEntry's message:
// PRIVMSGs, other commands, and highlights.
type MissedCounts struct {
	Messages   int64 `json:"messages"`
	Events     int64 `json:"events"`
	Highlights int64 `json:"highlights"`
}

// Missed counts what each entry's channel has had since its message, by
// connection then channel. Entries for connections
// user doesn't own are skipped, as are channels with nothing new.
func (d *DB) Missed(ctx context.Context, user string, es []MissedEntry) (map[string]map[string]MissedCounts, error) {
	out := map[string]map[string]MissedCounts{}
	if len(es) == 0 {
		return out, nil
	}
	conns := make([]string, len(es))
	chans := make([]string, len(es))
	after := make([]int64, len(es))
	for i, e := range es {
		conns[i], chans[i], after[i] = e.Connection, e.Channel, e.After
	}
	// unnest over three arrays doesn't fit the builder
	rows, err := d.Pool.Query(ctx, `
		SELECT ch.connection_id, ch.channel,
			SUM(CASE WHEN l.command = 'PRIVMSG' THEN 1 ELSE 0 END),
			SUM(CASE WHEN l.command <> 'PRIVMSG' THEN 1 ELSE 0 END),
			SUM(CASE WHEN l.highlight THEN 1 ELSE 0 END)
		FROM unnest($2::text[], $3::text[], $4::bigint[]) AS e(connection_id, channel, after)
		JOIN connections AS c ON c.id=e.connection_id AND c.user_id=$1
		JOIN channels AS ch ON ch.connection_id=e.connection_id AND ch.channel=e.channel
		JOIN logs AS l ON l.channel_id=ch.id AND l.id > e.after
		GROUP BY ch.id, ch.connection_id, ch.channel`, user, conns, chans, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var conn, channel string
		var n MissedCounts
		if err := rows.Scan(&conn, &channel, &n.Messages, &n.Events, &n.Highlights); err != nil {
			return nil, err
		}
		if out[conn] == nil {
			out[conn] = map[string]MissedCounts{}
		}
		out[conn][channel] = n
	}
	return out, rows.Err()
}

// collect is pgx.CollectRows, but never nil.
func collect[T any](rows pgx.Rows, fn pgx.RowToFunc[T]) ([]T, error) {
	out, err := pgx.CollectRows(rows, fn)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []T{}
	}
	return out, nil
}
