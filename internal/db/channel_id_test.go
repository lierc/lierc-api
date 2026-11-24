package db_test

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

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

	if err := d.Migrate(ctx, db.MigrateOptions{FS: migrationsUpTo(t, 10)}); err != nil {
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

// conversationsOf is each row of the four tables as "table connection
// channel", sorted: through the old columns before 0011 (old), through
// channel_id after it.
func conversationsOf(t *testing.T, d *db.DB, old bool) []string {
	t.Helper()
	var out []string
	for _, tbl := range channelIDTables {
		q := `SELECT c.connection_id || ' ' || c.channel FROM ` + tbl + ` x JOIN channels c ON c.id = x.channel_id`
		if old {
			q = `SELECT connection_id || ' ' || channel FROM ` + tbl
		}
		rows, err := d.Pool.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var v string
			rows.Scan(&v)
			out = append(out, tbl+" "+v)
		}
		rows.Close()
	}
	slices.Sort(out)
	return out
}

// Migration 0011 fills the channel_id the image before 0010 left empty
// (after a rollback), making the channels rows missing, then makes
// channel_id NOT NULL and drops the old columns, their indexes and
// seen_markers' old key.
func TestMigration11(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: migrationsUpTo(t, 9)}); err != nil {
		t.Fatal(err)
	}
	testenv.EnsureConnection(t, d, "c1", "u1")
	testenv.EnsureConnection(t, d, "c2", "u1")
	oldShapeRows(t, d)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: migrationsUpTo(t, 10)}); err != nil {
		t.Fatal(err)
	}
	// the old image again, after 0010: no channel_id, one conversation new
	exec(t, d, `INSERT INTO logs (id, connection_id, user_id, channel, time, message) VALUES
		(10, 'c1', 'u1', '#a', now(), '{}'), (11, 'c2', 'u1', '#new', now(), '{}')`)
	exec(t, d, `INSERT INTO highlights (id, connection_id, user_id, channel, time, message) VALUES (11, 'c2', 'u1', '#new', now(), '{}')`)
	exec(t, d, `INSERT INTO log_archives (connection_id, user_id, channel, first_id, last_id, min_time, max_time, count, messages)
		VALUES ('c2', 'u1', '#new', 2, 2, now(), now(), 1, '[]')`)
	exec(t, d, `INSERT INTO seen_markers (user_id, connection_id, channel, message_id) VALUES ('u1', 'c2', '#new', 11)`)
	want := conversationsOf(t, d, true)

	if err := d.Migrate(ctx, db.MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := conversationsOf(t, d, false); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("conversations after 0011:\n%v\nwant\n%v", got, want)
	}
	var cols []string
	rows, _ := d.Pool.Query(ctx, `SELECT table_name || '.' || column_name || ' ' || is_nullable FROM information_schema.columns
		WHERE table_name = ANY($1) AND column_name IN ('user_id', 'connection_id', 'channel', 'channel_id') ORDER BY 1`, channelIDTables)
	for rows.Next() {
		var c string
		rows.Scan(&c)
		cols = append(cols, c)
	}
	rows.Close()
	if want := "[highlights.channel_id NO log_archives.channel_id NO logs.channel_id NO seen_markers.channel_id NO]"; fmt.Sprint(cols) != want {
		t.Errorf("columns %v, want %s", cols, want)
	}
	var pkey string
	d.Pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'seen_markers_pkey'`).Scan(&pkey)
	if pkey != "PRIMARY KEY (channel_id)" {
		t.Errorf("seen_markers_pkey %q", pkey)
	}
	for _, idx := range newIndexes[:4] {
		if !indexExists(t, d, idx) {
			t.Errorf("index %s missing", idx)
		}
	}
	for _, idx := range []string{"logs_connection_id_channel_id_idx", "logs_user_id_highlight_idx", "logs_connection_id_msgid_idx",
		"log_archives_connection_id_channel_last_id_idx", "log_archives_user_id_idx", "highlights_user_id_id_idx", "seen_markers_connection_id_idx"} {
		if indexExists(t, d, idx) {
			t.Errorf("old index %s still there", idx)
		}
	}
}

// After 0010 the code before it can still insert (it leaves channel_id
// empty), so re-pinning the old image is a rollback.
func TestMigration10OldShapeInsert(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: migrationsUpTo(t, 10)}); err != nil {
		t.Fatal(err)
	}
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

// Logging into a conversation that has its channels row doesn't spend a
// channels id (the sequence is int4).
func TestChannelIDKeepsSequence(t *testing.T) {
	d := testenv.Postgres(t)
	testenv.EnsureConnection(t, d, "c1", "u1")
	e := db.LogEntry{Connection: "c1", Command: "PRIVMSG", Time: 1700000000,
		Message: []byte(`{"Command":"PRIVMSG","Params":["#a","hi"],"Tags":{"msgid":"m1"}}`)}
	if _, err := d.InsertLog(ctx, e, []string{"#a"}, false); err != nil {
		t.Fatal(err)
	}
	seq := func() int64 {
		var n int64
		d.Pool.QueryRow(ctx, `SELECT last_value FROM channels_id_seq`).Scan(&n)
		return n
	}
	before := seq()
	for range 20 {
		if _, err := d.InsertLog(ctx, e, []string{"#a"}, false); err != nil {
			t.Fatal(err)
		}
	}
	if after := seq(); after != before {
		t.Errorf("channels_id_seq went from %d to %d", before, after)
	}
}

// Seen for a conversation with no channels row (nothing logged there) is
// ignored: no row, no error, and no channels row made.
func TestSetSeenUnknownChannel(t *testing.T) {
	d := testenv.Postgres(t)
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	if err := d.SetSeen(ctx, a, "c1", "#nothing", 5); err != nil {
		t.Errorf("SetSeen: %v", err)
	}
	var seen, channels int
	d.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM seen_markers), (SELECT count(*) FROM channels)`).Scan(&seen, &channels)
	if seen != 0 || channels != 0 {
		t.Errorf("%d seen rows, %d channels rows", seen, channels)
	}
}

// Seen for someone else's connection is refused as for a gone one
// (ErrGone): the owner's marker stays.
func TestSetSeenOtherUsersConnection(t *testing.T) {
	d := testenv.Postgres(t)
	a, b := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob")
	testenv.AddConnection(t, d, "c1", a, "{}")
	testenv.ChannelID(t, d, "c1", "#a")
	if err := d.SetSeen(ctx, a, "c1", "#a", 5); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSeen(ctx, b, "c1", "#a", 9); !errors.Is(err, db.ErrGone) {
		t.Errorf("bob's SetSeen: %v, want ErrGone", err)
	}
	var id int
	d.Pool.QueryRow(ctx, `SELECT message_id FROM seen_markers`).Scan(&id)
	if id != 5 {
		t.Errorf("alice's marker is at %d, want 5", id)
	}
}

// readsFixture logs, for alice, on c1 and c2 (both hers): #a on both (one
// name, two connections), #b on c1; archives c1 #a's oldest messages into
// two blocks; and sets her seen marker on c1 #a.
func readsFixture(t *testing.T, d *db.DB) string {
	t.Helper()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	testenv.AddConnection(t, d, "c2", a, "{}")
	now := float64(time.Now().Unix())
	n := 0
	log := func(conn, channel, text string, highlight bool) {
		t.Helper()
		n++
		msg := fmt.Sprintf(`{"Command":"PRIVMSG","Params":[%q,%q],"Prefix":{"Name":"bob"},"Tags":{"msgid":"m%d"}}`, channel, text, n)
		if _, err := d.InsertLog(ctx, db.LogEntry{Connection: conn, Command: "PRIVMSG", Message: []byte(msg), Time: now}, []string{channel}, highlight); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 8 {
		log("c1", "#a", fmt.Sprint("hello one ", i), i%3 == 0)
		log("c2", "#a", fmt.Sprint("hello two ", i), i%4 == 0)
		log("c1", "#b", fmt.Sprint("hello bee ", i), false)
	}
	for range 2 {
		if _, ok, err := d.ArchiveBlock(ctx, "c1", "#a", 4, 2); !ok || err != nil {
			t.Fatalf("ArchiveBlock: %v %v", ok, err)
		}
	}
	if err := d.SetSeen(ctx, a, "c1", "#a", 10); err != nil {
		t.Fatal(err)
	}
	return a
}

// storeReads is every Store read of readsFixture's data, printed.
func storeReads(t *testing.T, d *db.DB, user string) map[string]string {
	t.Helper()
	out := map[string]string{}
	add := func(name string, v any, err error) {
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = fmt.Sprintf("%s", v)
	}
	today := time.Now().UTC().Format("2006-01-02")
	hello := "hello"
	var newest int32
	d.Pool.QueryRow(ctx, `SELECT max(id) FROM logs`).Scan(&newest)

	v1, err := d.ChannelLogs(ctx, "c1", "#a", 50)
	add("ChannelLogs c1", v1, err)
	v2, err := d.ChannelLogs(ctx, "c2", "#a", 50)
	add("ChannelLogs c2", v2, err)
	v3, err := d.ChannelLogsBefore(ctx, "c1", "#a", newest, 3)
	add("ChannelLogsBefore", v3, err)
	v4, err := d.ChannelDate(ctx, "c1", "#a", today, today, "UTC", nil, nil, 100)
	add("ChannelDate", v4, err)
	v5, err := d.ChannelLast(ctx, "c1", "#a", &hello, 50)
	add("ChannelLast", v5, err)
	m1, ok1, err := d.MessageByMsgid(ctx, "c1", "m22") // in logs
	add("MessageByMsgid logs", fmt.Sprint(m1, ok1), err)
	m2, ok2, err := d.MessageByMsgid(ctx, "c1", "m1") // archived
	add("MessageByMsgid block", fmt.Sprint(m2, ok2), err)
	c1, err := d.ArchiveCandidates(ctx, 2, 1, nil)
	add("ArchiveCandidates", fmt.Sprint(c1), err)
	c2, err := d.ArchiveCandidates(ctx, 2, 1, []string{"c2"})
	add("ArchiveCandidates c2", fmt.Sprint(c2), err)
	s1, err := d.Seen(ctx, user)
	add("Seen", fmt.Sprint(s1), err)
	n1, err := d.ChannelNames(ctx, "c1")
	add("ChannelNames c1", fmt.Sprint(n1), err)
	n2, err := d.ChannelNames(ctx, "c2")
	add("ChannelNames c2", fmt.Sprint(n2), err)
	mi, err := d.Missed(ctx, user, []db.MissedEntry{{Connection: "c1", Channel: "#a", After: 0},
		{Connection: "c2", Channel: "#a", After: 0}, {Connection: "c1", Channel: "#b", After: 20}})
	add("Missed", fmt.Sprint(mi), err)
	uc, err := d.UserCounts(ctx, user)
	add("UserCounts", fmt.Sprint(uc), err)
	cc, err := d.ConnectionCounts(ctx, "c1")
	add("ConnectionCounts c1", fmt.Sprint(cc), err)
	li, err := d.LatestMessageID(ctx, user)
	add("LatestMessageID", fmt.Sprint(li), err)
	ls, err := d.LogSince(ctx, user, 3)
	add("LogSince", ls, err)
	h1, err := d.Highlights(ctx, user, nil, 50)
	add("Highlights", h1, err)
	h2, err := d.Highlights(ctx, user, &newest, 2)
	add("Highlights before", h2, err)
	return out
}

// Every Store read finds readsFixture's rows through channel_id. (Until
// 0011 this also made the old columns lie and checked the reads didn't
// change, which is how step 1 was shown to read no old column.)
func TestReadsThroughChannelID(t *testing.T) {
	d := testenv.Postgres(t)
	user := readsFixture(t, d)
	for name, v := range storeReads(t, d, user) {
		if v == "[]" || strings.HasSuffix(v, "false") || v == "map[]" {
			t.Errorf("%s found nothing", name)
		}
	}
}

// Unread counts skip an entry whose conversation has no channels row and
// count the others.
func TestMissedUnknownChannel(t *testing.T) {
	d := testenv.Postgres(t)
	user := readsFixture(t, d)
	got, err := d.Missed(ctx, user, []db.MissedEntry{{Connection: "c1", Channel: "#b", After: 0}, {Connection: "c1", Channel: "#none", After: 0}})
	if err != nil || len(got) != 1 || len(got["c1"]) != 1 || got["c1"]["#b"].Messages != 8 {
		t.Errorf("Missed = %v, %v; want c1 #b's 8 only", got, err)
	}
}

// bruteIDs is the ids a plain query over every row of user's gives, newest
// first: the answer the per-channel reads must match.
func bruteIDs(t *testing.T, d *db.DB, table, user, where string, limit int, args ...any) []int64 {
	t.Helper()
	rows, err := d.Pool.Query(ctx, `SELECT x.id FROM `+table+` x JOIN channels ch ON ch.id = x.channel_id
		JOIN connections c ON c.id = ch.connection_id WHERE c.user_id = $1 AND `+where+`
		ORDER BY x.id DESC LIMIT `+fmt.Sprint(limit), append([]any{user}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	var out []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

// perUserFixture: alice's three channels on two connections and bob's
// one, with interleaved ids, 300 rows each (more than LogMax for alice), a
// third of them highlights.
func perUserFixture(t *testing.T, d *db.DB) (alice string) {
	t.Helper()
	a, b := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob")
	testenv.AddConnection(t, d, "c1", a, "{}")
	testenv.AddConnection(t, d, "c2", a, "{}")
	testenv.AddConnection(t, d, "c3", b, "{}")
	exec(t, d, `INSERT INTO logs (id, channel_id, command, highlight, time, message)
		SELECT g * 4 + k, test_channel_id(conn, chan), 'PRIVMSG', g % 3 = 0, now(), '{}'
		FROM generate_series(1, 300) g,
		(VALUES (0, 'c1', '#a'), (1, 'c2', '#a'), (2, 'c1', '#b'), (3, 'c3', '#a')) v(k, conn, chan)
		JOIN connections o ON o.id = v.conn, LATERAL (SELECT o.user_id AS owner) w`)
	exec(t, d, `INSERT INTO highlights (id, channel_id, time, message, self)
		SELECT id, channel_id, time, message, self FROM logs WHERE highlight`)
	return a
}

// The per-channel reads answer what a plain query over all the user's rows
// would.
func TestPerUserReadsMatchBruteForce(t *testing.T) {
	d := testenv.Postgres(t)
	a := perUserFixture(t, d)
	latest, err := d.LatestMessageID(ctx, a)
	if want := bruteIDs(t, d, "logs", a, "true", 1); err != nil || latest != want[0] {
		t.Errorf("LatestMessageID = %d, %v; want %d", latest, err, want[0])
	}
	for _, after := range []int32{0, 900, 1190} {
		got, err := d.LogSince(ctx, a, after)
		if err != nil {
			t.Fatal(err)
		}
		want := bruteIDs(t, d, "logs", a, "x.id > $2", db.LogMax, after)
		slices.Reverse(want)
		if !eq(ids(got, uid), want) {
			t.Errorf("LogSince(%d): %d ids %v..., want %d %v...", after, len(got), ids(got, uid)[:min(3, len(got))], len(want), want[:min(3, len(want))])
		}
	}
	for _, limit := range []int{1, 5, 1000} {
		for _, before := range []*int32{nil, ptr(int32(600)), ptr(int32(9))} {
			got, err := d.Highlights(ctx, a, before, limit)
			if err != nil {
				t.Fatal(err)
			}
			where, args := "true", []any{}
			if before != nil {
				where, args = "x.id < $2", []any{*before}
			}
			if want := bruteIDs(t, d, "highlights", a, where, limit, args...); !eq(ids(got, uid), want) {
				t.Errorf("Highlights(%v, %d) = %v, want %v", before, limit, ids(got, uid), want)
			}
		}
	}
	// each row with its own connection
	got, _ := d.LogSince(ctx, a, 1190)
	for _, m := range got {
		if want := []string{"c1", "c2", "c1"}[m.MessageId%4]; m.ConnectionId != want {
			t.Errorf("message %d on %s, want %s", m.MessageId, m.ConnectionId, want)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// explain is EXPLAIN (ANALYZE, COSTS OFF) of sql, as text.
func explain(t *testing.T, d *db.DB, sql string, args ...any) string {
	t.Helper()
	rows, err := d.Pool.Query(ctx, "EXPLAIN (ANALYZE, COSTS OFF) "+sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var line string
		rows.Scan(&line)
		plan += line + "\n"
	}
	return plan
}

// The per-user reads probe each channel's index, with far more channels
// than anyone has (500; production's most is 40), not read every row the
// user has.
func TestPerUserReadsPlans(t *testing.T) {
	d := testenv.Postgres(t)
	a, b := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob")
	testenv.AddConnection(t, d, "c1", a, "{}")
	testenv.AddConnection(t, d, "c2", b, "{}")
	for _, conn := range []string{"c1", "c2"} {
		exec(t, d, `INSERT INTO logs (channel_id, command, highlight, time, message)
			SELECT test_channel_id($1, '#c' || ch),
				'PRIVMSG', n % 8 = 0, now(), '{}'
			FROM generate_series(1, 40) n, generate_series(1, 500) ch`, conn)
	}
	exec(t, d, `INSERT INTO highlights (id, channel_id, time, message, self)
		SELECT id, channel_id, time, message, self FROM logs WHERE highlight`)
	exec(t, d, `ANALYZE`)
	before := int32(30000)
	for name, q := range map[string]func() (string, []any, error){
		"LatestMessageID":   func() (string, []any, error) { return db.LatestMessageIDSQL(a) },
		"LogSince":          func() (string, []any, error) { return db.LogSinceSQL(a, 100) },
		"Highlights":        func() (string, []any, error) { return db.HighlightsSQL(a, nil, 50) },
		"Highlights before": func() (string, []any, error) { return db.HighlightsSQL(a, &before, 50) },
	} {
		sql, args, err := q()
		if err != nil {
			t.Fatal(err)
		}
		plan := explain(t, d, sql, args...)
		table, idx := "logs", "logs_channel_id_id_idx"
		if strings.HasPrefix(name, "Highlights") {
			table, idx = "highlights", "highlights_channel_id_id_idx"
		}
		var ms float64
		if i := strings.Index(plan, "Execution Time: "); i >= 0 {
			fmt.Sscanf(plan[i:], "Execution Time: %f", &ms)
		}
		if strings.Contains(plan, "Seq Scan on "+table) || !strings.Contains(plan, idx) || ms == 0 || ms > 100 {
			t.Errorf("%s (%.1f ms) isn't a probe of %s per channel:\n%s", name, ms, idx, plan)
		}
	}
}

// 0011 runs after step 1's logger, which takes channels first (it looks
// the conversation up before inserting into logs), then logs, highlights
// and message_ids: 0011 locks them in that order, so a line in flight
// can't deadlock with it.
func TestMigration11LocksInLoggerOrder(t *testing.T) {
	sql, err := os.ReadFile("migrations/0011_drop_old_conversation_columns.sql")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^LOCK TABLE ([^;]+) IN ACCESS EXCLUSIVE MODE;`).FindSubmatch(sql)
	if m == nil {
		t.Fatal("no LOCK TABLE")
	}
	if got, want := string(m[1]), "channels, logs, highlights, message_ids, log_archives, seen_markers"; got != want {
		t.Errorf("0011 locks %s, want %s", got, want)
	}
}
