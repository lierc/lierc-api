package db_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestQueryStripsComments(t *testing.T) {
	q := db.Query("channel_date")
	if !strings.Contains(q, "SELECT") {
		t.Fatalf("channel_date = %q", q)
	}
	for _, line := range strings.Split(q, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			t.Errorf("comment line left: %q", line)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("unknown query didn't panic")
		}
	}()
	db.Query("nope")
}

// Typed text goes through websearch_to_tsquery: plain words, phrases,
// exclusions and punctuation never error.
func TestSearchQueriesTakePlainText(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	exec(t, d, `INSERT INTO log (connection, "user", channel, command, time, message) VALUES
		('c1', $1, '#a', 'PRIVMSG', now(), '{"Command":"PRIVMSG","Params":["#a","running late again"],"Prefix":{"Name":"bob"}}')`, a)
	// the database's own date: log.time is a timestamp in its time zone
	var today string
	if err := d.Pool.QueryRow(ctx, `SELECT current_date::text`).Scan(&today); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		q    string
		want int
	}{{"running late", 1}, {`"late again"`, 1}, {"run -nope", 1}, {"what?", 0}} {
		rows, err := d.Pool.Query(ctx, db.Query("channel_last"), "#a", "c1", c.q, 5)
		if err != nil {
			t.Fatalf("last %q: %v", c.q, err)
		}
		n := 0
		for rows.Next() {
			var id int64
			var msg, conn string
			var hl bool
			if err := rows.Scan(&id, &msg, &conn, &hl); err != nil {
				t.Fatal(err)
			}
			var m struct{ Params []string }
			json.Unmarshal([]byte(msg), &m)
			if len(m.Params) < 2 || !strings.Contains(m.Params[1], "\x02") {
				t.Errorf("last %q: no highlight in %s", c.q, msg)
			}
			n++
		}
		if err := rows.Err(); err != nil || n != c.want {
			t.Errorf("last %q: %d rows, %v; want %d", c.q, n, err, c.want)
		}

		rows, err = d.Pool.Query(ctx, db.Query("channel_date"), "c1", "#a", today, today, c.q, nil)
		if err != nil {
			t.Fatalf("date %q: %v", c.q, err)
		}
		n = 0
		for rows.Next() {
			n++
		}
		if err := rows.Err(); err != nil || n != c.want {
			t.Errorf("date %q: %d rows, %v; want %d", c.q, n, err, c.want)
		}
	}
}
