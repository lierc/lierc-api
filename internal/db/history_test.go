package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/archive"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

// msgRow inserts log row id on conn (owned by user) in channel.
func msgRow(t *testing.T, d *db.DB, id int64, conn, user, channel, text string, self, highlight bool, when string) {
	t.Helper()
	msg := fmt.Sprintf(`{"Command":"PRIVMSG","Params":[%q,%q],"Prefix":{"Name":"bob"}}`, channel, text)
	exec(t, d, `INSERT INTO logs (channel_id, id, connection_id, user_id, channel, command, self, highlight, time, message) VALUES (test_channel_id($2, $4), $1,$2,$3,$4,'PRIVMSG',$5,$6,$7::timestamp,$8)`,
		id, conn, user, channel, self, highlight, when, msg)
}

// oldMsgRow is msgRow on a database from before 0010 (no channel_id).
func oldMsgRow(t *testing.T, d *db.DB, id int64, conn, user, channel, text string, self, highlight bool, when string) {
	t.Helper()
	msg := fmt.Sprintf(`{"Command":"PRIVMSG","Params":[%q,%q],"Prefix":{"Name":"bob"}}`, channel, text)
	exec(t, d, `INSERT INTO logs (id, connection_id, user_id, channel, command, self, highlight, time, message) VALUES ($1,$2,$3,$4,'PRIVMSG',$5,$6,$7::timestamp,$8)`,
		id, conn, user, channel, self, highlight, when, msg)
}

func ids[T any](ms []T, id func(T) int64) []int64 {
	out := []int64{}
	for _, m := range ms {
		out = append(out, id(m))
	}
	return out
}

func uid(m db.UserMessage) int64 { return m.MessageId }
func mid(m db.Message) int64     { return m.MessageId }

func eq(a, b []int64) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

func TestLogSince(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a, b := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob")
	testenv.AddConnection(t, d, "c1", a, "{}")
	testenv.AddConnection(t, d, "bc", b, "{}")
	for i := int64(1); i <= 5; i++ {
		msgRow(t, d, i, "c1", a, "#a", fmt.Sprint("m", i), i == 4, i == 5, "2026-01-01")
	}
	msgRow(t, d, 6, "bc", b, "#a", "bob's", false, false, "2026-01-01")
	got, err := d.LogSince(ctx, a, 2)
	if err != nil || !eq(ids(got, uid), []int64{3, 4, 5}) {
		t.Fatalf("LogSince = %v, %v", ids(got, uid), err)
	}
	if !got[1].Self || got[1].Highlight || !got[2].Highlight || got[0].ConnectionId != "c1" {
		t.Errorf("flags = %+v", got)
	}
	var m struct{ Params []string }
	if json.Unmarshal(got[0].Message, &m) != nil || m.Params[1] != "m3" {
		t.Errorf("message = %s", got[0].Message)
	}
	for i := int64(10); i < 310; i++ {
		msgRow(t, d, i, "c1", a, "#a", "x", false, false, "2026-01-01")
	}
	got, err = d.LogSince(ctx, a, 9)
	if err != nil || len(got) != 251 || got[0].MessageId != 59 {
		t.Errorf("300 newer: %d rows, first %v, %v", len(got), ids(got, uid)[:1], err)
	}
	if got, _ := d.LogSince(ctx, b, 0); len(got) != 1 {
		t.Errorf("bob sees %v", ids(got, uid))
	}
}

func TestHighlights(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	for i := int64(1); i <= 6; i++ {
		if i%2 == 0 {
			hlRow(t, d, i, "c1", a, "#a", "x")
		} else {
			msgRow(t, d, i, "c1", a, "#a", "x", false, false, "2026-01-01")
		}
	}
	got, err := d.Highlights(ctx, a, nil, 2)
	if err != nil || !eq(ids(got, uid), []int64{6, 4}) {
		t.Errorf("Highlights = %v, %v", ids(got, uid), err)
	}
	before := int32(6)
	got, err = d.Highlights(ctx, a, &before, 50)
	if err != nil || !eq(ids(got, uid), []int64{4, 2}) {
		t.Errorf("before 6 = %v, %v", ids(got, uid), err)
	}
}

func TestChannelLogsAndBefore(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	for i := int64(1); i <= 6; i++ {
		msgRow(t, d, i, "c1", a, "#a", fmt.Sprint("m", i), false, false, "2020-01-01")
	}
	o := archive.Options{Keep: 2, Block: 2}
	for n := 0; ; n++ {
		_, ok, err := archive.MoveBlock(ctx, d, archive.Channel{Connection: "c1", Channel: "#a"}, o)
		if err != nil {
			t.Fatal(err)
		}
		if !ok || n > 10 {
			break
		}
	}
	var left int
	d.Pool.QueryRow(ctx, `SELECT count(*) FROM logs`).Scan(&left)
	if left != 2 {
		t.Fatalf("%d rows left in log, want 2 (archive didn't run)", left)
	}
	got, err := d.ChannelLogs(ctx, "c1", "#a", 5)
	if err != nil || !eq(ids(got, mid), []int64{6, 5, 4, 3, 2}) {
		t.Errorf("ChannelLogs = %v, %v", ids(got, mid), err)
	}
	got, err = d.ChannelLogsBefore(ctx, "c1", "#a", 3, 10)
	if err != nil || !eq(ids(got, mid), []int64{2, 1}) {
		t.Errorf("before 3 = %v, %v", ids(got, mid), err)
	}
	var m struct{ Params []string }
	if json.Unmarshal(got[0].Message, &m) != nil || m.Params[1] != "m2" {
		t.Errorf("archived message = %s", got[0].Message)
	}
}

func TestChannelLast(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	exec(t, d, `INSERT INTO logs (channel_id, connection_id, user_id, channel, command, time, message) VALUES (test_channel_id('c1', '#a'), 'c1',$1,'#a','PRIVMSG',now(),'{"Params":["#a","foo and bar"]}')`, a)
	if got, err := d.ChannelLast(ctx, "c1", "#a", nil, 5); err != nil || got == nil || len(got) != 0 {
		t.Errorf("nil query = %v, %v", got, err)
	}
	q := "foo bar"
	if got, err := d.ChannelLast(ctx, "c1", "#a", &q, 5); err != nil || len(got) != 1 {
		t.Errorf("foo bar = %v, %v", got, err)
	}
}

// ChannelDate's days are the time zone's: 03:00 UTC on 2 January is the
// evening of the 1st in New York, and the morning of the 3rd in Kiritimati
// (UTC+14) is 10:00 UTC on the 2nd.
func TestChannelDateTimeZone(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	msgRow(t, d, 1, "c1", a, "#a", "x", false, false, "2026-01-02 03:00+00")
	msgRow(t, d, 2, "c1", a, "#a", "x", false, false, "2026-01-02 10:00+00")
	for _, c := range []struct {
		tz, day string
		want    []int64
	}{
		{"UTC", "2026-01-02", []int64{1, 2}},
		{"America/New_York", "2026-01-01", []int64{1}},
		{"America/New_York", "2026-01-02", []int64{2}},
		{"Pacific/Kiritimati", "2026-01-02", []int64{1}},
		{"Pacific/Kiritimati", "2026-01-03", []int64{2}},
	} {
		got, err := d.ChannelDate(ctx, "c1", "#a", c.day, c.day, c.tz, nil, nil, 10)
		if err != nil || !eq(ids(got, mid), c.want) {
			t.Errorf("%s in %s = %v, %v; want %v", c.day, c.tz, ids(got, mid), err, c.want)
		}
	}
	if _, err := d.ChannelDate(ctx, "c1", "#a", "2026-01-02", "2026-01-02", "Mars/Olympus", nil, nil, 10); !errors.Is(err, db.ErrUnknownTimeZone) {
		t.Errorf("unknown zone: err = %v, want ErrUnknownTimeZone", err)
	}
}

// ChannelDate returns at most limit messages, the oldest.
func TestChannelDateLimit(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	for i := int64(1); i <= 3; i++ {
		msgRow(t, d, i, "c1", a, "#a", "x", false, false, "2026-01-02 10:00")
	}
	got, err := d.ChannelDate(ctx, "c1", "#a", "2026-01-01", "2026-01-03", "UTC", nil, nil, 10)
	if err != nil || !eq(ids(got, mid), []int64{1, 2, 3}) {
		t.Errorf("limit 10 = %v, %v", ids(got, mid), err)
	}
	got, err = d.ChannelDate(ctx, "c1", "#a", "2026-01-01", "2026-01-03", "UTC", nil, nil, 2)
	if err != nil || !eq(ids(got, mid), []int64{1, 2}) {
		t.Errorf("limit 2 = %v, %v", ids(got, mid), err)
	}
}

// msgidRow inserts log row id on conn in channel: command with IRC msgid.
// backfill runs migration 0008's SQL, which maps the logs rows that have no
// message_ids row (logged before 0007); it may run again, as here.
func backfill(t *testing.T, d *db.DB) {
	t.Helper()
	sql, err := fs.ReadFile(db.Migrations(), "migrations/0008_message_ids_from_logs.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(t, d, string(sql))
}

// msgidRow inserts a logs row as before message_ids (0007): not mapped
// until backfill.
func msgidRow(t *testing.T, d *db.DB, id int64, conn, user, channel, command, msgid string) {
	t.Helper()
	msg := fmt.Sprintf(`{"Command":%q,"Params":[%q,"text %d"],"Prefix":{"Name":"bob"},"Tags":{"msgid":%q}}`, command, channel, id, msgid)
	exec(t, d, `INSERT INTO logs (channel_id, id, connection_id, user_id, channel, command, time, message) VALUES (test_channel_id($2, $4), $1,$2,$3,$4,$5,now(),$6)`,
		id, conn, user, channel, command, msg)
}

// A reply's original is found by its connection and IRC msgid: the
// earliest PRIVMSG or NOTICE, never a NICK or QUIT logged per channel
// with the same msgid, and never another connection's.
func TestMessageByMsgid(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	b := testenv.AddUser(t, d, "bob")
	testenv.AddConnection(t, d, "c1", a, "{}")
	testenv.AddConnection(t, d, "c2", b, "{}")
	msgidRow(t, d, 1, "c1", a, "#a", "PRIVMSG", "m1")
	msgidRow(t, d, 2, "c1", a, "#a", "NICK", "n1")
	msgidRow(t, d, 3, "c1", a, "#b", "NICK", "n1")
	msgidRow(t, d, 4, "c1", a, "#a", "NOTICE", "m4")
	msgidRow(t, d, 5, "c2", b, "#a", "PRIVMSG", "m5")
	msgidRow(t, d, 6, "c1", a, "#b", "PRIVMSG", "m1")
	backfill(t, d)
	for _, tc := range []struct {
		conn, msgid string
		id          int64
		ok          bool
	}{
		{"c1", "m1", 1, true},
		{"c1", "m4", 4, true},
		{"c1", "n1", 0, false},
		{"c1", "m5", 0, false},
		{"c1", "nope", 0, false},
		{"c2", "m5", 5, true},
	} {
		m, ok, err := d.MessageByMsgid(ctx, tc.conn, tc.msgid)
		if err != nil || ok != tc.ok || (ok && m.MessageId != tc.id) {
			t.Errorf("%s %s: %d %v %v, want %d %v", tc.conn, tc.msgid, m.MessageId, ok, err, tc.id, tc.ok)
		}
	}
	m, _, _ := d.MessageByMsgid(ctx, "c1", "m1")
	var got struct{ Tags struct{ Msgid string } }
	if json.Unmarshal(m.Message, &got) != nil || got.Tags.Msgid != "m1" || m.ConnectionId != "c1" {
		t.Errorf("message = %s on %s", m.Message, m.ConnectionId)
	}
}

// A pre-0007 message, mapped by 0008, is still found once archived.
func TestMessageByMsgidArchived(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	msgidRow(t, d, 1, "c1", a, "#a", "PRIVMSG", "m1")
	msgidRow(t, d, 2, "c1", a, "#a", "PRIVMSG", "m2")
	backfill(t, d)
	if _, ok, err := archive.MoveBlock(ctx, d, archive.Channel{Connection: "c1", Channel: "#a"}, archive.Options{Keep: 1, Block: 1}); !ok || err != nil {
		t.Fatalf("archive: %v %v", ok, err)
	}
	wantFound(t, d, "c1", "m1", 1)
	wantFound(t, d, "c1", "m2", 2)
}

// logged logs a PRIVMSG with msgid on conn in channel through InsertLog,
// so message_ids is filled as in production, and returns its id.
func logged(t *testing.T, d *db.DB, conn, channel, msgid string) int64 {
	t.Helper()
	id, err := d.InsertLog(context.Background(), db.LogEntry{Connection: conn, Command: "PRIVMSG",
		Message: []byte(fmt.Sprintf(`{"Command":"PRIVMSG","Params":[%q,"text %s"],"Prefix":{"Name":"bob"},"Tags":{"msgid":%q}}`, channel, msgid, msgid)),
		Time:    1700000000}, []string{channel}, false)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func archiveAll(t *testing.T, d *db.DB, conn, channel string, keep, block int) {
	t.Helper()
	for n := 0; n < 20; n++ {
		_, ok, err := archive.MoveBlock(context.Background(), d, archive.Channel{Connection: conn, Channel: channel}, archive.Options{Keep: keep, Block: block})
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return
		}
	}
}

func wantFound(t *testing.T, d *db.DB, conn, msgid string, id int64) {
	t.Helper()
	m, ok, err := d.MessageByMsgid(context.Background(), conn, msgid)
	var got struct{ Tags struct{ Msgid string } }
	if err != nil || !ok || m.MessageId != id || json.Unmarshal(m.Message, &got) != nil || got.Tags.Msgid != msgid || m.ConnectionId != conn {
		t.Errorf("%s %s: %d %v %v %s, want id %d", conn, msgid, m.MessageId, ok, err, m.Message, id)
	}
}

// An original that has been archived is found through message_ids.
func TestMessageByMsgidAfterArchiving(t *testing.T) {
	d := testenv.Postgres(t)
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	id1, id2, id3 := logged(t, d, "c1", "#a", "m1"), logged(t, d, "c1", "#a", "m2"), logged(t, d, "c1", "#a", "m3")
	archiveAll(t, d, "c1", "#a", 1, 2)
	if n := count(t, d, "logs"); n != 1 {
		t.Fatalf("%d rows left in logs, want 1", n)
	}
	wantFound(t, d, "c1", "m1", id1)
	wantFound(t, d, "c1", "m2", id2)
	wantFound(t, d, "c1", "m3", id3)
}

// Blocks are per channel: an archived message is read from its own
// channel's block, with other channels' ids interleaved.
func TestMessageByMsgidOtherChannelsBlock(t *testing.T) {
	d := testenv.Postgres(t)
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	var ids = map[string]int64{}
	for i := 0; i < 4; i++ {
		for _, ch := range []string{"#a", "#b"} {
			k := fmt.Sprintf("%s-%d", ch[1:], i)
			ids[k] = logged(t, d, "c1", ch, k)
		}
	}
	archiveAll(t, d, "c1", "#a", 1, 3)
	archiveAll(t, d, "c1", "#b", 1, 3)
	wantFound(t, d, "c1", "b-1", ids["b-1"])
	wantFound(t, d, "c1", "a-2", ids["a-2"])
}

// Two connections (two lierc users) log the same msgid: each gets its own.
func TestMessageByMsgidOwnConnection(t *testing.T) {
	d := testenv.Postgres(t)
	a, b := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob")
	testenv.AddConnection(t, d, "c1", a, "{}")
	testenv.AddConnection(t, d, "c2", b, "{}")
	i1, i2 := logged(t, d, "c1", "#a", "x"), logged(t, d, "c2", "#a", "x")
	wantFound(t, d, "c1", "x", i1)
	wantFound(t, d, "c2", "x", i2)
}

// There's no fallback to logs: a row logged before message_ids (0007) is
// found only once 0008 has mapped it.
func TestMessageByMsgidNoFallback(t *testing.T) {
	d := testenv.Postgres(t)
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	msgidRow(t, d, 1, "c1", a, "#a", "PRIVMSG", "old")
	if _, ok, err := d.MessageByMsgid(context.Background(), "c1", "old"); ok || err != nil {
		t.Errorf("unmapped: %v %v, want not found", ok, err)
	}
	backfill(t, d)
	wantFound(t, d, "c1", "old", 1)
}

// Migration 0008 maps the PRIVMSG and NOTICE rows in logs that have a
// msgid and no message_ids row, under their own connection's channel;
// it skips other commands, maps nothing twice, and drops the logs msgid
// index.
func TestBackfillMessageIDs(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a, b := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob")
	testenv.AddConnection(t, d, "c1", a, "{}")
	testenv.AddConnection(t, d, "c2", b, "{}")
	msgidRow(t, d, 1001, "c1", a, "#a", "PRIVMSG", "p1")
	msgidRow(t, d, 1002, "c1", a, "#a", "NICK", "k1")
	msgidRow(t, d, 1003, "c1", a, "#b", "NOTICE", "n1")
	msgidRow(t, d, 1004, "c2", b, "#a", "PRIVMSG", "p1") // bob's copy
	exec(t, d, `INSERT INTO logs (channel_id, id, connection_id, user_id, channel, command, time, message) VALUES (test_channel_id('c1', '#a'), 1005, 'c1', $1, '#a', 'PRIVMSG', now(), '{"Command":"PRIVMSG","Params":["#a","no tags"]}')`, a)
	id6 := logged(t, d, "c1", "#a", "p6") // logged after 0007: mapped already
	backfill(t, d)
	backfill(t, d) // again: nothing twice
	// ordered by log_id: the logger's (from the sequence) before the explicit ids
	want := []string{fmt.Sprintf("c1 #a p6 %d", id6), "c1 #a p1 1001", "c1 #b n1 1003", "c2 #a p1 1004"}
	if got := msgidRows(t, d); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("message_ids:\n%v\nwant:\n%v", got, want)
	}
	var n int
	d.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname = 'logs_connection_id_msgid_idx'`).Scan(&n)
	if n != 0 {
		t.Error("logs_connection_id_msgid_idx is still there")
	}
}

// The archive read goes straight to its block through the last_id index,
// with a realistic number of blocks and no planner settings changed: one
// probe, not every block of the channel (the plan production showed for
// first_id <= id AND last_id >= id was a scan of all 3,010 of #lobsters').
func TestMessageByLogIDOneBlock(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	// 300 blocks of 500 ids in #a (and as many in #b), as lctl archive makes
	for _, ch := range []string{"#a", "#b"} {
		exec(t, d, `INSERT INTO log_archives (channel_id, connection_id, user_id, channel, first_id, last_id, min_time, max_time, count, messages)
			SELECT test_channel_id('c1', $2), 'c1', $1, $2, g * 1000 + o + 1, g * 1000 + o + 500, now(), now(), 500,
			  json_build_array(json_build_object('id', g * 1000 + o + 250, 'time', 0, 'highlight', false,
			    'message', json_build_object('Command', 'PRIVMSG', 'Params', json_build_array($2::text, 'x'))))
			FROM generate_series(0, 299) g, (SELECT CASE WHEN $2 = '#a' THEN 0 ELSE 500 END AS o) off`, a, ch)
	}
	exec(t, d, `ANALYZE log_archives`)
	var idx string
	if err := d.Pool.QueryRow(ctx, `SELECT indexname FROM pg_indexes WHERE tablename = 'log_archives' AND indexdef LIKE '%last_id%'`).Scan(&idx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{250, 150250, 299250} { // the oldest, a middle and the newest block
		sql, args, err := db.BlockHoldingSQL("c1", "#a", int32(id))
		if err != nil {
			t.Fatal(err)
		}
		rows, err := d.Pool.Query(ctx, "EXPLAIN (ANALYZE, COSTS OFF) "+sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan += line + "\n"
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan, "Index Scan") || !strings.Contains(plan, idx) || strings.Contains(plan, "Rows Removed by Filter: 2") {
			t.Errorf("id %d: the archive read isn't one probe of %s:\n%s", id, idx, plan)
		}
	}
	m, ok, err := d.MessageByMsgid(ctx, "c1", "nope")
	if ok || err != nil {
		t.Errorf("unknown msgid: %v %v %v", m, ok, err)
	}
}

// The lookups use their indexes.
func TestMessageByMsgidIndexes(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	for i := 0; i < 50; i++ {
		logged(t, d, "c1", "#a", fmt.Sprint("f", i))
	}
	exec(t, d, `INSERT INTO message_ids (channel_id, msgid, log_id) SELECT (SELECT id FROM channels LIMIT 1), 'g' || g, g + 100000 FROM generate_series(1, 3000) g`)
	exec(t, d, `ANALYZE message_ids`)
	exec(t, d, `ANALYZE log_archives`)
	plan := func(sql string, args ...any) string {
		rows, err := d.Pool.Query(ctx, "EXPLAIN "+sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var p string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			p += line + "\n"
		}
		return p
	}
	lookup, largs, err := db.MessageIDsLookupSQL("c1", "f3")
	if err != nil {
		t.Fatal(err)
	}
	if p := plan(lookup, largs...); !strings.Contains(p, "message_ids_msgid_idx") {
		t.Errorf("message_ids lookup doesn't use message_ids_msgid_idx:\n%s", p)
	}
	var idx string
	d.Pool.QueryRow(ctx, `SELECT indexname FROM pg_indexes WHERE tablename = 'log_archives' AND indexdef LIKE '%last_id%'`).Scan(&idx)
	// the archive is tiny here, so a sequential scan is the cheaper plan;
	// what matters is that the query can use the index at all
	c, err := d.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release()
	if _, err := c.Exec(ctx, "SET enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	defer c.Exec(ctx, "RESET enable_seqscan")
	bsql, bargs, err := db.BlockHoldingSQL("c1", "#a", 3)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Query(ctx, "EXPLAIN "+bsql, bargs...)
	if err != nil {
		t.Fatal(err)
	}
	var p string
	for rows.Next() {
		var line string
		rows.Scan(&line)
		p += line + "\n"
	}
	rows.Close()
	if idx == "" || !strings.Contains(p, idx) {
		t.Errorf("message_by_log_id can't use %q:\n%s", idx, p)
	}
}

// Backlog pages across logs and the archive match a brute-force answer —
// every id of the channel below before, newest first, the first limit —
// for every before and limit, with two channels' ids interleaved, several
// archive blocks (one straddling before), and the newest rows left in
// logs. A characterization of channel_logs(_before) before they moved to
// Go.
func TestChannelLogsPaging(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	var all []int64 // #a's ids, oldest first
	for i := 0; i < 37; i++ {
		for _, ch := range []string{"#a", "#b"} {
			id := logged(t, d, "c1", ch, fmt.Sprintf("%s-%d", ch[1:], i))
			if ch == "#a" {
				all = append(all, id)
			}
		}
	}
	archiveAll(t, d, "c1", "#a", 3, 5)
	archiveAll(t, d, "c1", "#b", 3, 5)
	// 6 full blocks of 5 per channel; 7 each left in logs (the newest 3,
	// and 4 short of a block)
	if n := count(t, d, "logs"); n != 14 {
		t.Fatalf("%d rows left in logs, want 14", n)
	}
	want := func(before int64, limit int) []int64 {
		out := []int64{}
		for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
			if all[i] < before {
				out = append(out, all[i])
			}
		}
		return out
	}
	check := func(what string, got []db.Message, err error, w []int64) {
		t.Helper()
		if err != nil || !eq(ids(got, mid), w) {
			t.Errorf("%s: %v %v, want %v", what, ids(got, mid), err, w)
			return
		}
		for _, m := range got {
			var msg struct{ Params []string }
			if json.Unmarshal(m.Message, &msg) != nil || len(msg.Params) < 1 || msg.Params[0] != "#a" || m.ConnectionId != "c1" {
				t.Errorf("%s: message %d is %s on %s", what, m.MessageId, m.Message, m.ConnectionId)
			}
		}
	}
	for _, limit := range []int{1, 2, 3, 4, 5, 6, 9, 10, 11, 37, 40} {
		got, err := d.ChannelLogs(ctx, "c1", "#a", limit)
		check(fmt.Sprintf("newest %d", limit), got, err, want(1<<30, limit))
		for before := all[0] - 1; before <= all[len(all)-1]+2; before++ {
			got, err := d.ChannelLogsBefore(ctx, "c1", "#a", int32(before), limit)
			check(fmt.Sprintf("before %d, %d", before, limit), got, err, want(before, limit))
		}
	}
}

// scripts/backfill-archived-message-ids.sql maps the PRIVMSGs and NOTICEs
// with a msgid in archive blocks from the last 90 days, so a reply to an
// archived message from before message_ids (0007) finds it; older blocks,
// other commands and lines without a msgid are left out, and nothing is
// mapped twice (it runs twice here).
func TestBackfillArchivedMessageIDs(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	block := func(first, last int, age string, messages string) {
		t.Helper()
		exec(t, d, `INSERT INTO log_archives (channel_id, connection_id, user_id, channel, first_id, last_id, min_time, max_time, count, messages)
			VALUES (test_channel_id('c1', '#a'), 'c1', $1, '#a', $2::int, $3::int, now() - $4::interval, now() - $4::interval, $3::int - $2::int + 1, $5::json)`, a, first, last, age, messages)
	}
	el := func(id int, command, tags string) string {
		return fmt.Sprintf(`{"id":%d,"time":0,"highlight":false,"message":{"Command":%q,"Params":["#a","x %d"],"Tags":{%s}}}`, id, command, id, tags)
	}
	block(1, 2, "200 days", "["+el(1, "PRIVMSG", `"msgid":"old"`)+","+el(2, "PRIVMSG", `"msgid":"old2"`)+"]")
	block(3, 7, "10 days", "["+el(3, "PRIVMSG", `"msgid":"r1"`)+","+el(4, "NOTICE", `"msgid":"r2"`)+","+
		el(5, "JOIN", `"msgid":"j1"`)+","+el(6, "PRIVMSG", "")+","+el(7, "PRIVMSG", `"msgid":"pre"`)+"]")
	// "pre" is mapped already (as if logged after 0007, then archived)
	exec(t, d, `INSERT INTO message_ids (channel_id, msgid, log_id) SELECT id, 'pre', 7 FROM channels`)

	sql, err := os.ReadFile("../../scripts/backfill-archived-message-ids.sql")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := d.Pool.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"c1 #a r1 3", "c1 #a r2 4", "c1 #a pre 7"}
	if got := msgidRows(t, d); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("message_ids:\n%v\nwant:\n%v", got, want)
	}
	m, ok, err := d.MessageByMsgid(ctx, "c1", "r1")
	if !ok || err != nil || m.MessageId != 3 {
		t.Errorf("r1: %v %v %v", m.MessageId, ok, err)
	}
	if _, ok, err := d.MessageByMsgid(ctx, "c1", "old"); ok || err != nil {
		t.Errorf("old (outside 90 days): %v %v, want not found", ok, err)
	}
}
