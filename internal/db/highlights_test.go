package db_test

import (
	"testing"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

// hlRow adds a highlighted message as the logger does: its log row, and
// its highlights row with the same id.
func hlRow(t *testing.T, d *db.DB, id int64, conn, user, channel, text string) {
	t.Helper()
	msgRow(t, d, id, conn, user, channel, text, false, true, "2026-01-01")
	exec(t, d, `INSERT INTO highlights (channel_id, id, connection_id, user_id, channel, time, message, self)
		SELECT test_channel_id(connection_id, channel), id, connection_id, user_id, channel, time, message, self FROM logs WHERE id = $1`, id)
}

// Migration 5 copies the highlighted rows of logs, with their columns.
func TestMigration5CopiesHighlights(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: migrationsUpTo(t, 4)}); err != nil {
		t.Fatal(err)
	}
	testenv.EnsureConnection(t, d, "c1", "u1")
	oldMsgRow(t, d, 1, "c1", "u1", "#a", "one", false, false, "2026-01-01")
	oldMsgRow(t, d, 2, "c1", "u1", "#a", "two", false, true, "2026-01-02")
	oldMsgRow(t, d, 3, "c1", "u1", "#b", "three", true, true, "2026-01-03")
	if err := d.Migrate(ctx, db.MigrateOptions{}); err != nil {
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
