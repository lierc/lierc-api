package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
