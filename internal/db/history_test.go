package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	exec(t, d, `INSERT INTO logs (connection_id, user_id, channel, command, time, message) VALUES ('c1',$1,'#a','PRIVMSG',now(),'{"Params":["#a","foo and bar"]}')`, a)
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
func msgidRow(t *testing.T, d *db.DB, id int64, conn, user, channel, command, msgid string) {
	t.Helper()
	msg := fmt.Sprintf(`{"Command":%q,"Params":[%q,"text %d"],"Prefix":{"Name":"bob"},"Tags":{"msgid":%q}}`, command, channel, id, msgid)
	exec(t, d, `INSERT INTO logs (id, connection_id, user_id, channel, command, time, message) VALUES ($1,$2,$3,$4,$5,now(),$6)`,
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

// Only logs is searched: an archived original isn't found.
func TestMessageByMsgidArchived(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	msgidRow(t, d, 1, "c1", a, "#a", "PRIVMSG", "m1")
	msgidRow(t, d, 2, "c1", a, "#a", "PRIVMSG", "m2")
	if _, ok, err := archive.MoveBlock(ctx, d, archive.Channel{Connection: "c1", Channel: "#a"}, archive.Options{Keep: 1, Block: 1}); !ok || err != nil {
		t.Fatalf("archive: %v %v", ok, err)
	}
	if _, ok, err := d.MessageByMsgid(ctx, "c1", "m1"); ok || err != nil {
		t.Errorf("archived m1: %v %v, want not found", ok, err)
	}
	if _, ok, err := d.MessageByMsgid(ctx, "c1", "m2"); !ok || err != nil {
		t.Errorf("m2: %v %v, want found", ok, err)
	}
}

// The lookup uses the msgid index, not a scan of logs.
func TestMessageByMsgidUsesIndex(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	exec(t, d, `INSERT INTO logs (id, connection_id, user_id, channel, command, time, message)
		SELECT g, 'c1', $1, '#a', 'PRIVMSG', now(), jsonb_build_object('Command','PRIVMSG','Params',jsonb_build_array('#a','x'),'Tags',jsonb_build_object('msgid','m'||g))
		FROM generate_series(1, 2000) g`, a)
	exec(t, d, `ANALYZE logs`)
	var plan string
	rows, err := d.Pool.Query(ctx, "EXPLAIN "+db.Query("message_by_msgid"), "c1", "m1")
	if err != nil {
		t.Fatal(err)
	}
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
	if !strings.Contains(plan, "logs_connection_id_msgid_idx") {
		t.Errorf("plan doesn't use the msgid index:\n%s", plan)
	}
}
