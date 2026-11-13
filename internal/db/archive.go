package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ChannelCount is a channel with Movable messages below the archive cutoff,
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

// below selects the ids of connection $1, channel $2 below the cutoff, given
// $3 = keep and $4 = the age cutoff, as CTEs named keep_id, age_id and below
// (oldest first, at most $5).
const below = `
keep_id AS (
	SELECT id FROM log WHERE connection = $1 AND channel = $2
	ORDER BY id DESC OFFSET $3::int - 1 LIMIT 1
),
age_id AS (
	SELECT id FROM log WHERE connection = $1 AND channel = $2 AND time < $4::timestamp
	ORDER BY id DESC LIMIT 1
),
below AS (
	SELECT id FROM log
	WHERE connection = $1 AND channel = $2
	  AND id < (SELECT id FROM keep_id) AND id <= (SELECT id FROM age_id)
	ORDER BY id LIMIT $5::int
)`

// pairsAll walks the distinct (connection, channel) pairs of log with a
// loose index scan over log (connection, channel, id DESC).
const pairsAll = `
pairs AS (
	(SELECT connection, channel FROM log ORDER BY connection, channel LIMIT 1)
	UNION ALL
	SELECT n.connection, n.channel FROM pairs p
	CROSS JOIN LATERAL (
		SELECT connection, channel FROM log
		WHERE (connection, channel) > (p.connection, p.channel)
		ORDER BY connection, channel LIMIT 1
	) n
)`

// pairsSome walks the channels of the connections in $4 the same way.
const pairsSome = `
pairs AS (
	SELECT f.connection, f.channel FROM unnest($4::text[]) c(id)
	CROSS JOIN LATERAL (
		SELECT connection, channel FROM log WHERE connection = c.id
		ORDER BY channel LIMIT 1
	) f
	UNION ALL
	SELECT n.connection, n.channel FROM pairs p
	CROSS JOIN LATERAL (
		SELECT connection, channel FROM log
		WHERE connection = p.connection AND channel > p.channel
		ORDER BY channel LIMIT 1
	) n
)`

// candidates counts, for each pair, the messages below its cutoff, up to
// $3 = a block, given $1 = keep and $2 = the age cutoff. Counting all of
// them would read every archivable row, which on a large backlog takes
// minutes; a block is all a caller needs to know.
const candidates = `
SELECT p.connection, p.channel, m.n
FROM pairs p
LEFT JOIN LATERAL (
	SELECT id FROM log WHERE connection = p.connection AND channel = p.channel
	ORDER BY id DESC OFFSET $1::int - 1 LIMIT 1
) k ON true
LEFT JOIN LATERAL (
	SELECT id FROM log WHERE connection = p.connection AND channel = p.channel AND time < $2::timestamp
	ORDER BY id DESC LIMIT 1
) a ON true
CROSS JOIN LATERAL (
	SELECT count(*)::int AS n FROM (
		SELECT 1 FROM log
		WHERE connection = p.connection AND channel = p.channel AND id < k.id AND id <= a.id
		LIMIT $3::int
	) b
) m
WHERE m.n > 0
ORDER BY p.connection, p.channel`

// ArchiveCandidates counts, for each (connection, channel) of log (of conns,
// when not empty), the messages below the cutoff, up to block: all but the
// newest keep messages, and no newer than the newest older than cutoff.
func (d *DB) ArchiveCandidates(ctx context.Context, keep int, cutoff time.Time, block int, conns []string) ([]ChannelCount, error) {
	sql := "WITH RECURSIVE " + pairsAll + candidates
	args := []any{keep, cutoff, block}
	if len(conns) > 0 {
		sql = "WITH RECURSIVE " + pairsSome + candidates
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
// one log_archive row, in one statement. It returns false, changing nothing,
// when fewer than block messages are archivable.
func (d *DB) ArchiveBlock(ctx context.Context, conn, channel string, keep int, cutoff time.Time, block int) (ArchivedBlock, bool, error) {
	var b ArchivedBlock
	err := d.Pool.QueryRow(ctx, `WITH `+below+`,
moved AS (
	DELETE FROM log
	WHERE id IN (SELECT id FROM below) AND (SELECT count(*) FROM below) = $5::int
	RETURNING id, "user", time, message, highlight
)
INSERT INTO log_archive (connection, "user", channel, first_id, last_id, min_time, max_time, count, messages)
SELECT $1, min("user"), $2, min(id), max(id), min(time), max(time), count(*),
	json_agg(json_build_object(
		'id', id,
		'time', floor(extract(epoch from time))::bigint,
		'message', message,
		'highlight', highlight
	) ORDER BY id)
FROM moved
HAVING count(*) > 0
RETURNING first_id, last_id, count`,
		conn, channel, keep, cutoff, block,
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
// cutoff of a channel, reporting what it deleted. It returns false when none
// are left.
func (d *DB) DropStatusBlock(ctx context.Context, conn, channel string, keep int, cutoff time.Time, block int) (ArchivedBlock, bool, error) {
	var b ArchivedBlock
	err := d.Pool.QueryRow(ctx, `WITH `+below+`,
dropped AS (
	DELETE FROM log WHERE id IN (SELECT id FROM below) RETURNING id
)
SELECT coalesce(min(id), 0), coalesce(max(id), 0), count(*)::int FROM dropped`,
		conn, channel, keep, cutoff, block,
	).Scan(&b.FirstID, &b.LastID, &b.Count)
	if err != nil {
		return ArchivedBlock{}, false, err
	}
	if b.Count == 0 {
		return ArchivedBlock{}, false, nil
	}
	return b, true, nil
}
