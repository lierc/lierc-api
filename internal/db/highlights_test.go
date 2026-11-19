package db_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

// hlRow adds a highlighted message as the logger does: its log row, and
// its highlights row with the same id.
func hlRow(t *testing.T, d *db.DB, id int64, conn, user, channel, text string) {
	t.Helper()
	msgRow(t, d, id, conn, user, channel, text, false, true, "2026-01-01")
	exec(t, d, `INSERT INTO highlights (id, connection_id, user_id, channel, time, message, self)
		SELECT id, connection_id, user_id, channel, time, message, self FROM logs WHERE id = $1`, id)
}

// Migration 5 copies the highlighted rows of logs, with their columns.
func TestMigration5CopiesHighlights(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: migrationsUpTo(t, 4), Rewrite: noLZ4}); err != nil {
		t.Fatal(err)
	}
	testenv.EnsureConnection(t, d, "c1", "u1")
	msgRow(t, d, 1, "c1", "u1", "#a", "one", false, false, "2026-01-01")
	msgRow(t, d, 2, "c1", "u1", "#a", "two", false, true, "2026-01-02")
	msgRow(t, d, 3, "c1", "u1", "#b", "three", true, true, "2026-01-03")
	if err := d.Migrate(ctx, db.MigrateOptions{Rewrite: noLZ4}); err != nil {
		t.Fatal(err)
	}

	rows, err := d.Pool.Query(ctx, `SELECT h.id FROM highlights h JOIN logs l USING (id)
		WHERE h.connection_id = l.connection_id AND h.user_id = l.user_id AND h.channel = l.channel
		  AND h.time = l.time AND h.message = l.message AND h.self = l.self
		ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		got = append(got, id)
	}
	rows.Close()
	var n int
	d.Pool.QueryRow(ctx, "SELECT count(*) FROM highlights").Scan(&n)
	if !eq(got, []int64{2, 3}) || n != 2 {
		t.Errorf("copied %v (of %d rows), want 2 and 3 matching their log rows", got, n)
	}
}

// The list reads the highlights table: a highlight whose log row was
// archived is still listed, and a log row's flag alone doesn't list it.
func TestHighlightsOutliveLogs(t *testing.T) {
	d := testenv.Postgres(t)
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	hlRow(t, d, 1, "c1", a, "#a", "old")
	hlRow(t, d, 2, "c1", a, "#a", "new")
	msgRow(t, d, 3, "c1", a, "#a", "flag only", false, true, "2026-01-01")
	exec(t, d, "DELETE FROM logs WHERE id = 1")

	got, err := d.Highlights(ctx, a, nil, 50)
	if err != nil || !eq(ids(got, uid), []int64{2, 1}) {
		t.Fatalf("Highlights = %v, %v; want 2, 1", ids(got, uid), err)
	}
	if !got[1].Highlight || got[1].ConnectionId != "c1" || string(got[1].Message) == "" {
		t.Errorf("archived one = %+v", got[1])
	}
}

// Deleting a connection deletes its highlights.
func TestHighlightsCascade(t *testing.T) {
	d := testenv.Postgres(t)
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	hlRow(t, d, 1, "c1", a, "#a", "x")
	exec(t, d, "DELETE FROM connections WHERE id = 'c1'")
	var n int
	d.Pool.QueryRow(ctx, "SELECT count(*) FROM highlights").Scan(&n)
	if n != 0 {
		t.Errorf("%d highlights left", n)
	}
}

// The backfill copies the archive's highlights, and logs' highlights the
// migration missed (written by the old logger after it), once: run again,
// it changes nothing, and it leaves highlights already copied alone.
func TestBackfillArchivedHighlights(t *testing.T) {
	d := testenv.Postgres(t)
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	hlRow(t, d, 9, "c1", a, "#a", "in logs")
	msgRow(t, d, 10, "c1", a, "#a", "after the migration", false, true, "2026-01-02")
	exec(t, d, `INSERT INTO log_archives (connection_id, user_id, channel, first_id, last_id, min_time, max_time, count, messages)
		VALUES ('c1', $1, '#a', 1, 3, '2026-01-01', '2026-01-01', 3, $2::json)`, a,
		`[{"id":1,"time":1767225600,"message":{"Command":"PRIVMSG","Params":["#a","plain"],"Prefix":{"Name":"bob"}},"highlight":false},
		  {"id":2,"time":1767225601,"message":{"Command":"PRIVMSG","Params":["#a","hey alice"],"Prefix":{"Name":"bob"}},"highlight":true},
		  {"id":3,"time":1767225602,"message":{"Command":"PRIVMSG","Params":["#a","me"],"Prefix":{"Name":"alice","Self":true}},"highlight":true}]`)
	sql, err := os.ReadFile("../../scripts/backfill-archived-highlights.sql")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := d.Pool.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.Highlights(ctx, a, nil, 50)
	if err != nil || !eq(ids(got, uid), []int64{10, 9, 3, 2}) {
		t.Fatalf("Highlights = %v, %v; want 10, 9, 3, 2", ids(got, uid), err)
	}
	if got[2].Self != true || got[3].Self || !strings.Contains(string(got[3].Message), "hey alice") {
		t.Errorf("copied %+v, %+v", got[2], got[3])
	}
	var at time.Time
	d.Pool.QueryRow(ctx, "SELECT time FROM highlights WHERE id = 2").Scan(&at)
	if at.Unix() != 1767225601 {
		t.Errorf("time %v", at)
	}
}
