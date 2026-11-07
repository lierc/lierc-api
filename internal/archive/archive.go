// Package archive moves old log rows into compressed blocks in log_archive,
// and deletes old status window messages.
//
// Per connection and channel, the archivable ids are those below the Keep-th
// newest message and no newer than the newest message older than MinAge.
// Channels move in full blocks of Block messages, oldest first; status
// windows are deleted in batches of up to Block messages instead.
package archive

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// statusChannel is the channel name the logger uses for status windows.
const statusChannel = "status"

type Options struct {
	Keep, Block int
	MinAge      time.Duration
	Now         time.Time
	// Connections limits Candidates to these connection ids; empty means all.
	Connections []string
}

type Channel struct {
	Connection, Channel string
	// Movable is the number of messages below the cutoff, counted only up
	// to a block.
	Movable int
	Status  bool
}

type Block struct {
	Connection, Channel    string
	FirstID, LastID, Count int
}

// ageCutoff is the time before which messages are old enough to archive.
// log.time holds UTC wall-clock times.
func (o Options) ageCutoff() time.Time {
	n := o.Now
	if n.IsZero() {
		n = time.Now()
	}
	return n.UTC().Add(-o.MinAge)
}

func (o Options) check() error {
	if o.Keep < 1 || o.Block < 1 {
		return fmt.Errorf("archive: keep (%d) and block (%d) must be positive", o.Keep, o.Block)
	}
	return nil
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

// Candidates lists the channels with at least a block of archivable messages,
// and the status windows with any messages below the cutoff.
func Candidates(ctx context.Context, db *pgxpool.Pool, o Options) ([]Channel, error) {
	if err := o.check(); err != nil {
		return nil, err
	}
	sql := "WITH RECURSIVE " + pairsAll + candidates
	args := []any{o.Keep, o.ageCutoff(), o.Block}
	if len(o.Connections) > 0 {
		sql = "WITH RECURSIVE " + pairsSome + candidates
		args = append(args, o.Connections)
	}
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cs []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.Connection, &c.Channel, &c.Movable); err != nil {
			return nil, err
		}
		c.Status = c.Channel == statusChannel
		if c.Movable >= o.Block || c.Status {
			cs = append(cs, c)
		}
	}
	return cs, rows.Err()
}

// MoveBlock moves the oldest Block archivable messages of c into one
// log_archive row, in one statement. It returns false, changing nothing,
// when fewer than Block messages are archivable.
func MoveBlock(ctx context.Context, db *pgxpool.Pool, c Channel, o Options) (Block, bool, error) {
	if err := o.check(); err != nil {
		return Block{}, false, err
	}
	if c.Channel == statusChannel {
		return Block{}, false, errors.New("archive: status windows are dropped, not archived")
	}
	b := Block{Connection: c.Connection, Channel: c.Channel}
	err := db.QueryRow(ctx, `WITH `+below+`,
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
		c.Connection, c.Channel, o.Keep, o.ageCutoff(), o.Block,
	).Scan(&b.FirstID, &b.LastID, &b.Count)
	if errors.Is(err, pgx.ErrNoRows) {
		return Block{}, false, nil
	}
	if err != nil {
		return Block{}, false, err
	}
	return b, true, nil
}

// DropStatus deletes up to Block of the oldest messages below the cutoff of
// the status window c, reporting what it deleted. It returns false when none
// are left.
func DropStatus(ctx context.Context, db *pgxpool.Pool, c Channel, o Options) (Block, bool, error) {
	if err := o.check(); err != nil {
		return Block{}, false, err
	}
	if c.Channel != statusChannel {
		return Block{}, false, fmt.Errorf("archive: %s is not a status window", c.Channel)
	}
	b := Block{Connection: c.Connection, Channel: c.Channel}
	err := db.QueryRow(ctx, `WITH `+below+`,
dropped AS (
	DELETE FROM log WHERE id IN (SELECT id FROM below) RETURNING id
)
SELECT coalesce(min(id), 0), coalesce(max(id), 0), count(*)::int FROM dropped`,
		c.Connection, c.Channel, o.Keep, o.ageCutoff(), o.Block,
	).Scan(&b.FirstID, &b.LastID, &b.Count)
	if err != nil {
		return Block{}, false, err
	}
	if b.Count == 0 {
		return Block{}, false, nil
	}
	return b, true, nil
}
