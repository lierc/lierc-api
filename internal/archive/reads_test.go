package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

// readSQL loads one of the channel read queries (internal/db/sql).
func readSQL(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "db", "sql", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type readRow struct {
	ID         int
	Message    any // decoded, so equal messages compare equal whatever their text
	Connection string
	Highlight  bool
}

type fixtureMsg struct {
	conn, channel string
	at            time.Time
}

func ts(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05.999999", s)
	if err != nil {
		panic(err)
	}
	return t
}

// readFixture inserts messages for c1 #a, c1 #b and c2 #a whose ids
// interleave in time order, spanning September into October, with
// messages at 23:59:59.7 and 00:00:00.2 either side of two midnights.
// It returns the ids inserted.
func readFixture(d *tdb) []int {
	d.t.Helper()
	chans := map[[2]string][]string{
		{"c1", "#a"}: {
			"2026-09-29 21:00:00", "2026-09-30 09:15:00",
			"2026-09-30 23:59:59.7", "2026-10-01 00:00:00.2", // one block, across a midnight and a month
			"2026-10-01 12:00:00", "2026-10-01 18:30:00",
			"2026-10-02 03:00:00", "2026-10-02 15:45:00",
			"2026-10-03 08:00:00", "2026-10-03 23:00:00",
			"2026-10-04 01:00:00", "2026-10-04 10:00:00", "2026-10-04 20:00:00",
		},
		{"c1", "#b"}: {
			"2026-09-29 21:30:00", "2026-09-30 10:00:00",
			"2026-09-30 22:00:00", "2026-10-01 02:00:00",
			"2026-10-01 23:59:59.7", "2026-10-02 00:00:00.2", // one block, across a midnight
			"2026-10-02 09:00:00", "2026-10-02 21:00:00",
			"2026-10-03 04:00:00", "2026-10-03 16:00:00",
			"2026-10-04 05:00:00", "2026-10-04 18:00:00",
		},
		{"c2", "#a"}: {
			"2026-09-30 11:00:00", "2026-10-01 13:00:00", "2026-10-02 16:00:00", "2026-10-03 12:00:00",
		},
	}
	var msgs []fixtureMsg
	for k, times := range chans {
		for _, s := range times {
			msgs = append(msgs, fixtureMsg{k[0], k[1], ts(s)})
		}
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].at.Before(msgs[j].at) })

	texts := []string{
		"running late again", "I run every day", "she runs fast", "nothing to see",
		"the cat sat", `héllo ☃ "quoted"`, "Running? Ran! run.", "cats and dogs",
		"late night", "it's fine & dandy",
	}
	var idList []int
	for i, m := range msgs {
		id := i + 1
		sender := "alice"
		if i%3 == 1 {
			sender = "bob"
		}
		msg, err := json.Marshal(map[string]any{
			"Command": "PRIVMSG",
			"Params":  []string{m.channel, texts[i%len(texts)]},
			"Prefix":  map[string]any{"Name": sender, "User": sender, "Host": "example.com", "Self": i%5 == 0},
			"Time":    m.at.Format(time.RFC3339Nano),
		})
		if err != nil {
			d.t.Fatal(err)
		}
		testenv.EnsureConnection(d.t, d.db, m.conn, "u-"+m.conn)
		d.exec(`INSERT INTO logs (id, connection_id, user_id, channel, command, highlight, self, time, message)
			VALUES ($1, $2::text, 'u-' || $2::text, $3, 'PRIVMSG', $4, $5, $6::timestamp, $7)`,
			id, m.conn, m.channel, id%3 == 0, i%5 == 0, m.at.Format("2006-01-02 15:04:05.999999"), string(msg))
		idList = append(idList, id)
	}
	return idList
}

// readCase is a read run before and after archiving: raw SQL (the
// channel read queries still in internal/db/sql), or a db.DB method (the
// backlog pages, which moved into Go).
type readCase struct {
	name string
	sql  string
	args []any
	page func(*db.DB) ([]db.Message, error)
}

func strp(s string) *string { return &s }

func readCases(t *testing.T, fixtureIDs []int) []readCase {
	dateSQL := readSQL(t, "channel_date.sql")

	pairs := [][2]string{{"c1", "#a"}, {"c1", "#b"}, {"c2", "#a"}, {"c1", "#none"}}
	var cs []readCase
	for _, p := range pairs {
		conn, ch := p[0], p[1]
		for _, limit := range []int{1, 3, 10, 100} {
			cs = append(cs, readCase{
				name: fmt.Sprintf("logs %s %s limit %d", conn, ch, limit),
				page: func(d *db.DB) ([]db.Message, error) { return d.ChannelLogs(context.Background(), conn, ch, limit) },
			})
		}
		cursors := append([]int{}, fixtureIDs...)
		cursors = append(cursors, fixtureIDs[len(fixtureIDs)-1]+1)
		for _, before := range cursors {
			for _, limit := range []int{1, 2, 5} {
				cs = append(cs, readCase{
					name: fmt.Sprintf("logs_before %s %s before %d limit %d", conn, ch, before, limit),
					page: func(d *db.DB) ([]db.Message, error) {
						return d.ChannelLogsBefore(context.Background(), conn, ch, int32(before), limit)
					},
				})
			}
		}

		ranges := [][2]string{
			{"2026-09-29", "2026-10-04"}, // everything
			{"2026-09-29", "2026-09-30"}, // ends between 23:59:59.7 and 00:00:00.2, inside a block
			{"2026-10-01", "2026-10-02"},
			{"2026-09-01", "2026-09-30"}, // ends at the month boundary
			{"2026-10-01", "2026-10-31"},
		}
		for d := ts("2026-09-28 00:00:00"); !d.After(ts("2026-10-05 00:00:00")); d = d.AddDate(0, 0, 1) {
			s := d.Format("2006-01-02")
			ranges = append(ranges, [2]string{s, s})
		}
		type filter struct{ text, sender *string }
		filters := []filter{
			{},
			{text: strp("run")}, // matches running, runs, Running
			{text: strp("cat")},
			{text: strp("late & run")},
			{text: strp("héllo")},
			{text: strp("zebra")},
			{sender: strp("alice")},
			{sender: strp("bob")},
			{sender: strp("nobody")},
			{text: strp("run"), sender: strp("alice")},
			{text: strp("late"), sender: strp("bob")},
		}
		// the days of the search's time zone, apart from the session's
		dateZones := []string{"UTC", "America/New_York"}
		for _, r := range ranges {
			for _, f := range filters {
				for _, tz := range dateZones {
					var text, sender any
					desc := ""
					if f.text != nil {
						text = *f.text
						desc += " text " + *f.text
					}
					if f.sender != nil {
						sender = *f.sender
						desc += " sender " + *f.sender
					}
					if tz != "UTC" {
						desc += " in " + tz
					}
					cs = append(cs, readCase{
						name: fmt.Sprintf("date %s %s %s..%s%s", conn, ch, r[0], r[1], desc),
						sql:  dateSQL, args: []any{conn, ch, r[0], r[1], text, sender, 1 << 20, tz},
					})
				}
			}
		}
	}
	return cs
}

func runRead(t *testing.T, conn *pgxpool.Conn, sql string, args []any) []readRow {
	t.Helper()
	rows, err := conn.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
	defer rows.Close()
	out := []readRow{}
	for rows.Next() {
		var r readRow
		var msg string
		if err := rows.Scan(&r.ID, &msg, &r.Connection, &r.Highlight); err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(strings.NewReader(msg))
		dec.UseNumber()
		if err := dec.Decode(&r.Message); err != nil {
			t.Fatalf("message %q: %v", msg, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// runPage is a db.DB read's rows as runRead gives them.
func runPage(t *testing.T, d *db.DB, page func(*db.DB) ([]db.Message, error)) []readRow {
	t.Helper()
	ms, err := page(d)
	if err != nil {
		t.Fatal(err)
	}
	out := []readRow{}
	for _, m := range ms {
		r := readRow{ID: int(m.MessageId), Connection: m.ConnectionId, Highlight: m.Highlight}
		dec := json.NewDecoder(strings.NewReader(string(m.Message)))
		dec.UseNumber()
		if err := dec.Decode(&r.Message); err != nil {
			t.Fatalf("message %q: %v", m.Message, err)
		}
		out = append(out, r)
	}
	return out
}

func rowIDs(rs []readRow) []int {
	ids := []int{}
	for _, r := range rs {
		ids = append(ids, r.ID)
	}
	return ids
}

var readZones = []string{"UTC", "America/New_York"}

// runAll runs every case in every session time zone.
func runAll(t *testing.T, d *tdb, cases []readCase) map[string][]readRow {
	t.Helper()
	got := map[string][]readRow{}
	for _, zone := range readZones {
		runZone(t, d, zone, cases, got)
	}
	return got
}

// runZone is runAll's cases in one time zone. The connection is released
// however it ends: held past a t.Fatal, it would leave the pool's Close,
// at cleanup, waiting for it forever.
func runZone(t *testing.T, d *tdb, zone string, cases []readCase, got map[string][]readRow) {
	t.Helper()
	conn, err := d.pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(context.Background(), "SET TIME ZONE '"+zone+"'"); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), "RESET TIME ZONE")
	for _, c := range cases {
		if c.page != nil {
			got[zone+" "+c.name] = runPage(t, d.db, c.page)
			continue
		}
		got[zone+" "+c.name] = runRead(t, conn, c.sql, c.args)
	}
}

func TestReadsSameAfterArchiving(t *testing.T) {
	d := newDB(t)
	fixtureIDs := readFixture(d)
	cases := readCases(t, fixtureIDs)

	before := runAll(t, d, cases)

	// The fixture must give the cases something to find.
	nonEmpty := 0
	for _, rs := range before {
		if len(rs) > 0 {
			nonEmpty++
		}
	}
	if nonEmpty < len(before)/3 {
		t.Fatalf("only %d of %d cases return rows", nonEmpty, len(before))
	}
	// Every filter that should match finds something in the whole range,
	// in both channels of c1.
	for _, ch := range []string{"#a", "#b"} {
		for _, f := range []string{"", " text run", " text cat", " sender alice", " sender bob", " text run sender alice"} {
			k := "UTC date c1 " + ch + " 2026-09-29..2026-10-04" + f
			if rs, ok := before[k]; !ok || len(rs) == 0 {
				t.Fatalf("%q returns no rows", k)
			}
		}
	}
	for _, q := range []string{"run", "cat", "late & run", "héllo"} {
		if d.count(`SELECT count(*) FROM logs WHERE to_tsvector('english', message->'Params'->1) @@ to_tsquery($1)`, q) == 0 {
			t.Fatalf("no message matches %q", q)
		}
	}
	if d.count(`SELECT count(*) FROM logs WHERE message->'Params'->>1 ILIKE 'running%' AND to_tsvector('english', message->'Params'->1) @@ to_tsquery('run')`) == 0 {
		t.Fatal("run does not match running")
	}

	for _, p := range [][2]string{{"c1", "#a"}, {"c1", "#b"}, {"c2", "#a"}} {
		moveAll(t, d, Channel{Connection: p[0], Channel: p[1]}, opts())
	}
	if got := d.archiveBlocks("c1", "#a"); len(got) != 5 {
		t.Fatalf("c1 #a blocks %v", got)
	}
	if got := d.archiveBlocks("c1", "#b"); len(got) != 4 {
		t.Fatalf("c1 #b blocks %v", got)
	}
	if n := d.count(`SELECT count(*) FROM logs WHERE connection_id = 'c1'`); n != 7 {
		t.Fatalf("%d c1 rows left in log", n)
	}
	// Blocks that span midnight, so day ranges end inside them.
	if n := d.count(`SELECT count(*) FROM log_archives WHERE min_time::date <> max_time::date`); n < 2 {
		t.Fatalf("%d blocks span a midnight", n)
	}

	after := runAll(t, d, cases)
	for _, zone := range readZones {
		for _, c := range cases {
			k := zone + " " + c.name
			if !reflect.DeepEqual(after[k], before[k]) {
				t.Errorf("%s (%s): after archiving ids %v, before %v", c.name, zone, rowIDs(after[k]), rowIDs(before[k]))
			}
		}
	}
	t.Logf("%d cases in %d time zones, %d returning rows", len(cases), len(readZones), nonEmpty)
}
