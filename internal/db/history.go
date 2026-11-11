package db

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// The history reads, ported from lib/API/Controller/{Message,Channel}.pm.
// Message is the stored JSON as it is (jsonb's key order, not Perl's).

// Message is a channel message as the channel reads send it.
type Message struct {
	MessageId    int64
	Message      json.RawMessage
	ConnectionId string
	Highlight    bool
}

// UserMessage is a message as /log and /highlight send it, with Self.
type UserMessage struct {
	MessageId    int64
	Message      json.RawMessage
	ConnectionId string
	Self         bool
	Highlight    bool
}

// LogMax is how many newer messages LogSince returns at most; the API
// refuses when it returns more than LogMax-1.
const LogMax = 251

// LogSince is user's messages with ids above after, oldest first, at most
// LogMax of them (Message.pm log).
func (d *DB) LogSince(ctx context.Context, user string, after int32) ([]UserMessage, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT * FROM (
			SELECT l.id, l.message::text, l.connection, l.self, l.highlight
			FROM log AS l
			JOIN connection AS c ON c.id = l.connection
			WHERE c."user" = $1 AND l.id > $2
			ORDER BY l.id DESC
			LIMIT $3
		) newest ORDER BY id ASC`, user, after, LogMax)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanUserMessage)
}

// Highlights is user's highlighted messages, newest first, below before
// when it is set (Message.pm highlight).
func (d *DB) Highlights(ctx context.Context, user string, before *int32, limit int) ([]UserMessage, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT l.id, l.message::text, l.connection, l.self, l.highlight
		FROM log AS l
		WHERE l."user" = $1 AND l.highlight = true
		  AND ($2::int IS NULL OR l.id < $2::int)
		ORDER BY l.id DESC
		LIMIT $3`, user, before, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanUserMessage)
}

// ChannelLogs is the newest limit messages of channel on conn, from log
// and log_archive, newest first (Channel.pm logs).
func (d *DB) ChannelLogs(ctx context.Context, conn, channel string, limit int) ([]Message, error) {
	return d.messages(ctx, Query("channel_logs"), channel, conn, limit)
}

// ChannelLogsBefore is ChannelLogs below message before (Channel.pm
// logs_id).
func (d *DB) ChannelLogsBefore(ctx context.Context, conn, channel string, before int32, limit int) ([]Message, error) {
	return d.messages(ctx, Query("channel_logs_before"), channel, conn, before, limit)
}

// ChannelLast is the last week's PRIVMSGs in channel matching query, with
// the match marked (Channel.pm last). A nil query matches nothing.
func (d *DB) ChannelLast(ctx context.Context, conn, channel string, query *string, limit int) ([]Message, error) {
	return d.messages(ctx, Query("channel_last"), channel, conn, query, limit)
}

// ChannelDate calls each for the messages of channel on conn from date
// from through date to, oldest first, as they're read; text and sender
// filter when set (Channel.pm date). It stops with each's error.
func (d *DB) ChannelDate(ctx context.Context, conn, channel, from, to string, text, sender *string, each func(Message) error) error {
	rows, err := d.Pool.Query(ctx, Query("channel_date"), conn, channel, from, to, text, sender)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return err
		}
		if err := each(m); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (d *DB) messages(ctx context.Context, sql string, args ...any) ([]Message, error) {
	rows, err := d.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanMessage)
}

func scanMessage(row pgx.CollectableRow) (Message, error) {
	var m Message
	var msg string
	err := row.Scan(&m.MessageId, &msg, &m.ConnectionId, &m.Highlight)
	m.Message = json.RawMessage(msg)
	return m, err
}

func scanUserMessage(row pgx.CollectableRow) (UserMessage, error) {
	var m UserMessage
	var msg string
	err := row.Scan(&m.MessageId, &msg, &m.ConnectionId, &m.Self, &m.Highlight)
	m.Message = json.RawMessage(msg)
	return m, err
}
