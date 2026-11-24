package db_test

import (
	"fmt"
	"testing"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

// channelIDTables are the tables that name their conversation by
// channel_id since 0010.
var channelIDTables = []string{"logs", "log_archives", "highlights", "seen_markers"}

// oldShapeRows inserts, as the code before 0010 did, a row in each of the
// four tables: logs on c1 #a, c2 #a (one name on two connections), c1 nick
// (a private chat) and c1 status; a highlight on c1 #a; an archive block
// on c1 #old (nothing in logs); a seen marker on c2 #seen-only.
func oldShapeRows(t *testing.T, d *db.DB) {
	t.Helper()
	exec(t, d, `INSERT INTO logs (id, connection_id, user_id, channel, time, message) VALUES
		(1, 'c1', 'u1', '#a', now(), '{}'), (2, 'c2', 'u1', '#a', now(), '{}'),
		(3, 'c1', 'u1', 'nick', now(), '{}'), (4, 'c1', 'u1', 'status', now(), '{}')`)
	exec(t, d, `INSERT INTO highlights (id, connection_id, user_id, channel, time, message) VALUES (1, 'c1', 'u1', '#a', now(), '{}')`)
	exec(t, d, `INSERT INTO log_archives (connection_id, user_id, channel, first_id, last_id, min_time, max_time, count, messages)
		VALUES ('c1', 'u1', '#old', 1, 1, now(), now(), 1, '[]')`)
	exec(t, d, `INSERT INTO seen_markers (user_id, connection_id, channel, message_id) VALUES ('u1', 'c2', '#seen-only', 2)`)
}

// rowCounts is each of the four tables' row count.
func rowCounts(t *testing.T, d *db.DB) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, tbl := range channelIDTables {
		var n int
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM `+tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[tbl] = n
	}
	return out
}

// badChannelIDs counts the rows of table whose channel_id doesn't name
// their own (connection_id, channel): NULL or another conversation's.
func badChannelIDs(t *testing.T, d *db.DB, table string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` x
		LEFT JOIN channels c ON c.id = x.channel_id
		WHERE c.id IS NULL OR c.connection_id <> x.connection_id OR c.channel <> x.channel`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func indexExists(t *testing.T, d *db.DB, name string) bool {
	t.Helper()
	var n int
	d.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname = $1`, name).Scan(&n)
	return n == 1
}

// newIndexes are the channel_id indexes 0010 adds.
var newIndexes = []string{
	"logs_channel_id_id_idx", "log_archives_channel_id_last_id_idx", "log_archives_channel_id_min_time_idx",
	"highlights_channel_id_id_idx", "seen_markers_channel_id_key",
}

// Migration 0010 gives every row of the four tables the channel_id of its
// conversation, making the channels rows that were missing, and keeps the
// old columns and indexes.
func TestMigration10FillsChannelID(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: migrationsUpTo(t, 9)}); err != nil {
		t.Fatal(err)
	}
	testenv.EnsureConnection(t, d, "c1", "u1")
	testenv.EnsureConnection(t, d, "c2", "u1")
	var before int32
	if err := d.Pool.QueryRow(ctx, `INSERT INTO channels (connection_id, channel) VALUES ('c1', '#a') RETURNING id`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	oldShapeRows(t, d)
	counts := rowCounts(t, d)

	if err := d.Migrate(ctx, db.MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := rowCounts(t, d); fmt.Sprint(got) != fmt.Sprint(counts) {
		t.Errorf("row counts %v, want %v", got, counts)
	}
	for _, tbl := range channelIDTables {
		if n := badChannelIDs(t, d, tbl); n != 0 {
			t.Errorf("%s: %d rows without their own channel_id", tbl, n)
		}
	}
	var after int32
	d.Pool.QueryRow(ctx, `SELECT id FROM channels WHERE connection_id = 'c1' AND channel = '#a'`).Scan(&after)
	if after != before {
		t.Errorf("c1 #a's channels id %d, was %d", after, before)
	}
	for _, idx := range append(newIndexes, "logs_connection_id_channel_id_idx", "logs_user_id_highlight_idx", "seen_markers_pkey") {
		if !indexExists(t, d, idx) {
			t.Errorf("index %s missing", idx)
		}
	}
}

// After 0010 the code before it can still insert (it leaves channel_id
// empty), so re-pinning the old image is a rollback.
func TestMigration10OldShapeInsert(t *testing.T) {
	d := testenv.Postgres(t)
	testenv.EnsureConnection(t, d, "c1", "u1")
	testenv.EnsureConnection(t, d, "c2", "u1")
	oldShapeRows(t, d)
	for _, tbl := range channelIDTables {
		var n int
		d.Pool.QueryRow(ctx, `SELECT count(*) FROM `+tbl+` WHERE channel_id IS NULL`).Scan(&n)
		if n == 0 {
			t.Errorf("%s: no old-shape row with an empty channel_id", tbl)
		}
	}
}
