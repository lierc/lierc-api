package db_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

// Migration 4's type changes don't rewrite logs or log_archives (964 MB in
// production) or their other indexes. The two indexes on a changed time
// column are rebuilt (timestamp_ops -> timestamptz_ops): logs_time_idx, a
// quarter of a second at production's 188k rows, and the archive's
// min_time index, one entry per block.
func TestMigration4NoRewrite(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: migrationsUpTo(t, 3)}); err != nil {
		t.Fatal(err)
	}
	// the tables and every index on them, by name
	filenodes := func() map[string]int64 {
		rows, err := d.Pool.Query(ctx, `SELECT c.relname, pg_relation_filenode(c.oid)::bigint FROM pg_class c
			WHERE c.relname IN ('logs', 'log_archives')
			   OR c.oid IN (SELECT indexrelid FROM pg_index WHERE indrelid IN ('logs'::regclass, 'log_archives'::regclass))`)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		for rows.Next() {
			var name string
			var node int64
			if err := rows.Scan(&name, &node); err != nil {
				t.Fatal(err)
			}
			out[name] = node
		}
		return out
	}
	before := filenodes()
	if err := d.Migrate(ctx, db.MigrateOptions{FS: migrationsUpTo(t, 4)}); err != nil {
		t.Fatal(err)
	}
	after := filenodes()
	rebuilt := map[string]bool{"logs_time_idx": true, "log_archives_connection_id_channel_min_time_idx": true}
	for name, node := range before {
		switch {
		case name == "logs_sender": // dropped
		case rebuilt[name] && after[name] == node:
			t.Errorf("%s wasn't rebuilt; the test's expectation is stale", name)
		case !rebuilt[name] && after[name] != node:
			t.Errorf("%s was rebuilt", name)
		}
	}
}

// (on the schema before 0011, which dropped the channel columns and the
// (connection_id, user_id) keys)
func TestStricterColumns(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: migrationsUpTo(t, 10)}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"users.last_login", "users.registered", "users.verify_sent", "connections.created",
		"connections.updated", "logs.time", "log_archives.min_time", "log_archives.max_time", "private_chats.time",
		"seen_markers.updated"} {
		if got := columnType(t, d, c); got != "timestamp with time zone" {
			t.Errorf("%s is %s", c, got)
		}
	}
	for _, c := range []string{"logs.channel", "log_archives.channel", "seen_markers.channel"} {
		if got := columnType(t, d, c); got != "text" {
			t.Errorf("%s is %s", c, got)
		}
	}
	for _, c := range []string{"logs.highlight", "connections.liercd"} {
		if one[string](t, d, fmt.Sprintf(`SELECT is_nullable FROM information_schema.columns WHERE table_name || '.' || column_name = '%s'`, c)) != "NO" {
			t.Errorf("%s allows NULL", c)
		}
	}
	if n := one[int](t, d, `SELECT count(*) FROM pg_class WHERE relname = 'logs_sender'`); n != 0 {
		t.Error("logs_sender still exists")
	}
	for _, table := range []string{"logs", "log_archives", "seen_markers"} {
		n := one[int](t, d, fmt.Sprintf(`SELECT count(*) FROM pg_constraint WHERE contype = 'f' AND conrelid = '%s'::regclass
			AND confrelid = 'connections'::regclass AND confdeltype = 'c' AND array_length(conkey, 1) = 2`, table))
		if n != 1 {
			t.Errorf("%s: %d (connection_id, user_id) keys, want 1", table, n)
		}
	}
}

func columnType(t *testing.T, d *db.DB, col string) string {
	t.Helper()
	return one[string](t, d, fmt.Sprintf(`SELECT data_type FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name || '.' || column_name = '%s'`, col))
}

// The constraints refuse what the code never writes, and only that (on the
// schema before 0011; channels has the same lowercase CHECK).
func TestStricterColumnsBite(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: migrationsUpTo(t, 10)}); err != nil {
		t.Fatal(err)
	}
	u := testenv.AddUser(t, d, "u")
	other := testenv.AddUser(t, d, "other")
	testenv.AddConnection(t, d, "c1", u, `{}`)
	logRow := func(user, channel, highlight string) string {
		return fmt.Sprintf(`INSERT INTO logs (connection_id, user_id, channel, time, message, highlight)
			VALUES ('c1', '%s', '%s', now(), '{}', %s)`, user, channel, highlight)
	}
	for sql, code := range map[string]string{
		logRow(other, "#a", "false"):                           "23503",
		logRow(u, "#Foo", "false"):                             "23514",
		logRow(u, "#a", "NULL"):                                "23502",
		`UPDATE connections SET liercd = '' WHERE id = 'c1'`:   "23514",
		`UPDATE connections SET liercd = NULL WHERE id = 'c1'`: "23502",
	} {
		_, err := d.Pool.Exec(ctx, sql)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != code {
			t.Errorf("%s: err = %v, want SQLSTATE %s", sql, err, code)
		}
	}
	// only ASCII capitals are refused
	for _, channel := range []string{"#café", "#Éclair"} {
		if _, err := d.Pool.Exec(ctx, logRow(u, channel, "false")); err != nil {
			t.Errorf("%s: %v", channel, err)
		}
	}
}
