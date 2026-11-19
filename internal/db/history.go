package db

import (
	"context"
	"encoding/json"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
)

// The history reads. Message is the stored JSON as it is, in jsonb's key
// order.

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
// LogMax of them.
func (d *DB) LogSince(ctx context.Context, user string, after int32) ([]UserMessage, error) {
	newest := userMessages(sub).Join("connections AS c ON c.id = l.connection_id").
		Where(sq.Eq{"c.user_id": user}).Where(sq.Gt{"l.id": after}).
		OrderBy("l.id DESC").Limit(LogMax)
	rows, err := query(ctx, d.Pool, psql.Select("*").FromSelect(newest, "newest").OrderBy("id ASC"))
	if err != nil {
		return nil, err
	}
	return collect(rows, scanUserMessage)
}

// Highlights is user's highlighted messages, newest first, below before
// when it is set. They're read from highlights, not logs, so they're kept
// when their log rows are archived.
func (d *DB) Highlights(ctx context.Context, user string, before *int32, limit int) ([]UserMessage, error) {
	q := psql.Select("l.id", "l.message::text", "l.connection_id", "l.self", "true").From("highlights AS l").
		Where(sq.Eq{"l.user_id": user})
	if before != nil {
		q = q.Where(sq.Lt{"l.id": *before})
	}
	rows, err := query(ctx, d.Pool, q.OrderBy("l.id DESC").Suffix("LIMIT ?", limit))
	if err != nil {
		return nil, err
	}
	return collect(rows, scanUserMessage)
}

// ChannelLogs is the newest limit messages of channel on conn, from log
// and log_archives, newest first.
func (d *DB) ChannelLogs(ctx context.Context, conn, channel string, limit int) ([]Message, error) {
	return d.messages(ctx, Query("channel_logs"), channel, conn, limit)
}

// ChannelLogsBefore is ChannelLogs below message before.
func (d *DB) ChannelLogsBefore(ctx context.Context, conn, channel string, before int32, limit int) ([]Message, error) {
	return d.messages(ctx, Query("channel_logs_before"), channel, conn, before, limit)
}

// ChannelLast is the last week's PRIVMSGs in channel matching query, with
// the match marked. A nil query matches nothing.
func (d *DB) ChannelLast(ctx context.Context, conn, channel string, query *string, limit int) ([]Message, error) {
	return d.messages(ctx, Query("channel_last"), channel, conn, query, limit)
}

// ChannelDate is the messages of channel on conn from date from through
// date to, oldest first, at most limit of them; text and sender filter
// when set.
func (d *DB) ChannelDate(ctx context.Context, conn, channel, from, to string, text, sender *string, limit int) ([]Message, error) {
	return d.messages(ctx, Query("channel_date"), conn, channel, from, to, text, sender, limit)
}

func (d *DB) messages(ctx context.Context, sql string, args ...any) ([]Message, error) {
	rows, err := d.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanMessage)
}

// userMessages selects a UserMessage's columns from log AS l, with b (psql,
// or sub for a subquery).
func userMessages(b sq.StatementBuilderType) sq.SelectBuilder {
	return b.Select("l.id", "l.message::text", "l.connection_id", "l.self", "l.highlight").From("logs AS l")
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
