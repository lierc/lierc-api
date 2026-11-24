package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ChannelCount is a channel with Movable messages older than its newest keep,
// counted only up to a block.
type ChannelCount struct {
	Connection, Channel string
	Movable             int
}

// ArchivedBlock is the id range and size of a block of messages moved out
// of log (or deleted from a status window).
type ArchivedBlock struct {
	FirstID, LastID, Count int
}

// below selects the ids of connection $1, channel $2 older than its
// newest $3 = keep, as CTEs named ch (its channels row), keep_id and below
// (oldest first, at most $4).
const below = `
ch AS (
	SELECT id FROM channels WHERE connection_id = $1 AND channel = $2
),
keep_id AS (
	SELECT id FROM logs WHERE channel_id = (SELECT id FROM ch)
	ORDER BY id DESC OFFSET $3::int - 1 LIMIT 1
),
below AS (
	SELECT id FROM logs
	WHERE channel_id = (SELECT id FROM ch) AND id < (SELECT id FROM keep_id)
	ORDER BY id LIMIT $4::int
)`

// pairsAll is every channels row; pairsSome those of the connections in
// $3.
const (
	pairsAll  = `pairs AS (SELECT id, connection_id, channel FROM channels)`
	pairsSome = `pairs AS (SELECT id, connection_id, channel FROM channels WHERE connection_id = ANY($3::text[]))`
)

// candidates counts, for each pair, the messages older than its newest
// $1 = keep, up to $2 = a block, through logs (channel_id, id DESC).
// Counting all of them would read every archivable row, which on a large
// backlog takes minutes; a block is all a caller needs to know.
const candidates = `
SELECT p.connection_id, p.channel, m.n
FROM pairs p
LEFT JOIN LATERAL (
	SELECT id FROM logs WHERE channel_id = p.id
	ORDER BY id DESC OFFSET $1::int - 1 LIMIT 1
) k ON true
CROSS JOIN LATERAL (
	SELECT count(*)::int AS n FROM (
		SELECT 1 FROM logs
		WHERE channel_id = p.id AND id < k.id
		LIMIT $2::int
	) b
) m
WHERE m.n > 0
ORDER BY p.connection_id, p.channel`

// ArchiveCandidates counts, for each channel (of conns, when not empty),
// the messages older than its newest keep, up to block.
func (d *DB) ArchiveCandidates(ctx context.Context, keep int, block int, conns []string) ([]ChannelCount, error) {
	sql := "WITH " + pairsAll + candidates
	args := []any{keep, block}
	if len(conns) > 0 {
		sql = "WITH " + pairsSome + candidates
		args = append(args, conns)
	}
	rows, err := d.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cs []ChannelCount
	for rows.Next() {
		var c ChannelCount
		if err := rows.Scan(&c.Connection, &c.Channel, &c.Movable); err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	return cs, rows.Err()
}

// ArchiveBlock moves the oldest block archivable messages of a channel into
// one log_archives row, in one statement. It returns false, changing nothing,
// when fewer than block messages are archivable.
func (d *DB) ArchiveBlock(ctx context.Context, conn, channel string, keep int, block int) (ArchivedBlock, bool, error) {
	var b ArchivedBlock
	err := d.Pool.QueryRow(ctx, `WITH `+below+`,
moved AS (
	DELETE FROM logs
	WHERE id IN (SELECT id FROM below) AND (SELECT count(*) FROM below) = $4::int
	RETURNING id, time, message, highlight
)
INSERT INTO log_archives (channel_id, first_id, last_id, min_time, max_time, count, messages)
SELECT (SELECT id FROM ch), min(id), max(id), min(time), max(time), count(*),
	json_agg(json_build_object(
		'id', id,
		'time', floor(extract(epoch from time))::bigint,
		'message', message,
		'highlight', highlight
	) ORDER BY id)
FROM moved
HAVING count(*) > 0
RETURNING first_id, last_id, count`,
		conn, channel, keep, block,
	).Scan(&b.FirstID, &b.LastID, &b.Count)
	if errors.Is(err, pgx.ErrNoRows) {
		return ArchivedBlock{}, false, nil
	}
	if err != nil {
		return ArchivedBlock{}, false, err
	}
	return b, true, nil
}

// DropStatusBlock deletes up to block of the oldest messages below the
// keep of a channel, reporting what it deleted. It returns false when none
// are left.
func (d *DB) DropStatusBlock(ctx context.Context, conn, channel string, keep int, block int) (ArchivedBlock, bool, error) {
	var b ArchivedBlock
	err := d.Pool.QueryRow(ctx, `WITH `+below+`,
dropped AS (
	DELETE FROM logs WHERE id IN (SELECT id FROM below) RETURNING id
)
SELECT coalesce(min(id), 0), coalesce(max(id), 0), count(*)::int FROM dropped`,
		conn, channel, keep, block,
	).Scan(&b.FirstID, &b.LastID, &b.Count)
	if err != nil {
		return ArchivedBlock{}, false, err
	}
	if b.Count == 0 {
		return ArchivedBlock{}, false, nil
	}
	return b, true, nil
}
