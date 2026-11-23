package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	return d.channelPage(ctx, conn, channel, nil, limit)
}

// ChannelLogsBefore is ChannelLogs below message before.
func (d *DB) ChannelLogsBefore(ctx context.Context, conn, channel string, before int32, limit int) ([]Message, error) {
	return d.channelPage(ctx, conn, channel, &before, limit)
}

// channelPage is up to limit messages of channel on conn below before (any,
// when nil), newest first: from logs, then, while the page isn't full, from
// the channel's archive blocks, newest first, unpacked one at a time
// (archived ids are all below the ids still in logs). One read-only
// snapshot, so a nightly archive run can't move rows between the reads.
func (d *DB) channelPage(ctx context.Context, conn, channel string, before *int32, limit int) ([]Message, error) {
	tx, err := d.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	q := psql.Select("id", "message::text", "connection_id", "highlight").From("logs").
		Where(sq.Eq{"connection_id": conn, "channel": channel})
	if before != nil {
		q = q.Where(sq.Lt{"id": *before})
	}
	rows, err := query(ctx, tx, q.OrderBy("id DESC").Limit(uint64(limit)))
	if err != nil {
		return nil, err
	}
	page, err := collect(rows, scanMessage)
	if err != nil {
		return nil, err
	}

	// the block below before: the one straddling it, else the newest
	// ending below it (any, without before); then each next older one
	cursor := before
	var block blockRef
	ok := false
	if len(page) < limit {
		if before != nil {
			block, ok, err = blockHolding(ctx, tx, conn, channel, *before)
			if err == nil && (!ok || block.first >= *before) {
				block, ok, err = blockBelow(ctx, tx, conn, channel, before)
			}
		} else {
			block, ok, err = blockBelow(ctx, tx, conn, channel, nil)
		}
	}
	for ; err == nil && ok && len(page) < limit; block, ok, err = blockBelow(ctx, tx, conn, channel, &block.first) {
		e := archivedMessages(block.id)
		if cursor != nil {
			e = e.Where("(e->>'id')::int < ?", *cursor)
		}
		rows, err := query(ctx, tx, e.OrderBy("1 DESC").Limit(uint64(limit-len(page))))
		if err != nil {
			return nil, err
		}
		more, err := collect(rows, scanMessage)
		if err != nil {
			return nil, err
		}
		page = append(page, more...)
		cursor = nil // the blocks after the first are wholly below it
	}
	if err != nil {
		return nil, err
	}
	return page, tx.Commit(ctx)
}

// blockRef is an archive block: its row id and the first message id in it.
type blockRef struct {
	id, first int32
}

// blockHolding is the archive block of channel on conn whose range holds
// message id: the first ending at or after it (a channel's blocks never
// overlap), one probe of the (connection_id, channel, last_id) index. ok is
// false when none ends at or after it; a block found may still start
// after id (id isn't archived).
func blockHolding(ctx context.Context, r runner, conn, channel string, id int32) (blockRef, bool, error) {
	return oneBlock(ctx, r, blockHoldingQuery(conn, channel, id))
}

func blockHoldingQuery(conn, channel string, id int32) sq.SelectBuilder {
	return psql.Select("id", "first_id").From("log_archives").
		Where(sq.Eq{"connection_id": conn, "channel": channel}).Where(sq.GtOrEq{"last_id": id}).
		OrderBy("last_id").Limit(1)
}

// blockBelow is the newest archive block of channel on conn ending below
// id (the newest of all, when id is nil): one probe of the same index.
func blockBelow(ctx context.Context, r runner, conn, channel string, id *int32) (blockRef, bool, error) {
	q := psql.Select("id", "first_id").From("log_archives").Where(sq.Eq{"connection_id": conn, "channel": channel})
	if id != nil {
		q = q.Where(sq.Lt{"last_id": *id})
	}
	return oneBlock(ctx, r, q.OrderBy("last_id DESC").Limit(1))
}

func oneBlock(ctx context.Context, r runner, q sq.SelectBuilder) (blockRef, bool, error) {
	var b blockRef
	err := queryRow(ctx, r, q).Scan(&b.id, &b.first)
	if errors.Is(err, pgx.ErrNoRows) {
		return blockRef{}, false, nil
	}
	return b, err == nil, err
}

// archivedMessages selects the messages of archive block id, unpacked, as
// Message's columns; callers add filters and order.
func archivedMessages(block int32) sq.SelectBuilder {
	return psql.Select("(e->>'id')::int", "(e->'message')::text", "a.connection_id", "(e->>'highlight')::bool").
		From("log_archives AS a").JoinClause("CROSS JOIN LATERAL json_array_elements(a.messages) e").
		Where(sq.Eq{"a.id": block})
}

// ChannelLast is the last week's PRIVMSGs in channel matching query, with
// the match marked. A nil query matches nothing.
func (d *DB) ChannelLast(ctx context.Context, conn, channel string, query *string, limit int) ([]Message, error) {
	return d.messages(ctx, Query("channel_last"), channel, conn, query, limit)
}

// MessageByMsgid is conn's PRIVMSG or NOTICE with IRC msgid msgid, for a
// reply's original; ok is false when there's none. message_ids (never
// archived; 0008 mapped what was in logs before it) says where it was
// logged, and it's read from logs, else unpacked from the archive block
// holding it.
func (d *DB) MessageByMsgid(ctx context.Context, conn, msgid string) (Message, bool, error) {
	var channel string
	var logID int32
	err := queryRow(ctx, d.Pool, messageIDsLookup(conn, msgid)).Scan(&channel, &logID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, err
	}
	chat := sq.Eq{"command": []string{"PRIVMSG", "NOTICE"}}
	if m, ok, err := d.oneMessage(ctx, psql.Select("id", "message::text", "connection_id", "highlight").From("logs").
		Where(sq.Eq{"id": logID, "connection_id": conn}).Where(chat)); ok || err != nil {
		return m, ok, err
	}
	block, ok, err := blockHolding(ctx, d.Pool, conn, channel, logID)
	if err != nil || !ok || block.first > logID {
		return Message{}, false, err
	}
	return d.oneMessage(ctx, archivedMessages(block.id).
		Where("(e->>'id')::int = ?", logID).
		Where("e->'message'->>'Command' IN ('PRIVMSG', 'NOTICE')"))
}

// messageIDsLookup is where conn's copy of msgid was logged, its channel
// and logs id, from message_ids through message_ids_msgid_idx: each
// connection logs its own copy, so the msgid alone isn't enough; the
// earliest if it was logged twice.
func messageIDsLookup(conn, msgid string) sq.SelectBuilder {
	return psql.Select("c.channel", "m.log_id").From("message_ids AS m").
		Join("channels AS c ON c.id = m.channel_id").
		Where(sq.Eq{"m.msgid": msgid, "c.connection_id": conn}).
		OrderBy("m.log_id").Limit(1)
}

// oneMessage is the first Message q selects, ok false when none.
func (d *DB) oneMessage(ctx context.Context, q sq.SelectBuilder) (Message, bool, error) {
	rows, err := query(ctx, d.Pool, q.Limit(1))
	if err != nil {
		return Message{}, false, err
	}
	ms, err := collect(rows, scanMessage)
	if err != nil || len(ms) == 0 {
		return Message{}, false, err
	}
	return ms[0], true, nil
}

// ErrUnknownTimeZone is ChannelDate's error for a time zone Postgres
// doesn't know.
var ErrUnknownTimeZone = errors.New("unknown time zone")

// ChannelDate is the messages of channel on conn from date from through
// date to, days in time zone tz (an IANA name), oldest first, at most limit
// of them; text and sender filter when set.
func (d *DB) ChannelDate(ctx context.Context, conn, channel, from, to, tz string, text, sender *string, limit int) ([]Message, error) {
	ms, err := d.messages(ctx, Query("channel_date"), conn, channel, from, to, text, sender, limit, tz)
	var pe *pgconn.PgError
	// invalid_parameter_value: time zone "..." not recognized
	if errors.As(err, &pe) && pe.Code == "22023" && strings.Contains(pe.Message, "time zone") {
		return nil, ErrUnknownTimeZone
	}
	return ms, err
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
