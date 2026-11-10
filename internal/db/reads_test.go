package db_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

func exec(t *testing.T, d *db.DB, sql string, args ...any) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// logRow inserts a log row on conn (owned by user) in channel.
func logRow(t *testing.T, d *db.DB, id int64, conn, user, channel string, command any, highlight bool) {
	t.Helper()
	exec(t, d, `INSERT INTO log (id, connection, "user", channel, command, highlight, time, message) VALUES ($1,$2,$3,$4,$5,$6,now(),'{}')`,
		id, conn, user, channel, command, highlight)
}

func TestPrefs(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a, b, c := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob"), testenv.AddUser(t, d, "carol")
	exec(t, d, `INSERT INTO pref ("user", name, value) VALUES ($1,'theme','"dark"'), ($1,'email','true'), ($2,'theme','"x"')`, a, b)
	got, err := d.Prefs(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	want := map[db.Pref]bool{{Name: "theme", Value: `"dark"`}: true, {Name: "email", Value: "true"}: true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Errorf("alice prefs = %v", got)
	}
	if got, err := d.Prefs(ctx, c); err != nil || got == nil || len(got) != 0 {
		t.Errorf("carol prefs = %#v, %v", got, err)
	}
}

func TestPrivates(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a, b, c := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob"), testenv.AddUser(t, d, "carol")
	testenv.AddConnection(t, d, "c1", a, "{}")
	testenv.AddConnection(t, d, "c2", b, "{}")
	exec(t, d, `INSERT INTO private (connection, nick, time) VALUES
		('c1','newer','2026-10-30 01:00:00'), ('c1','bob','2026-10-29 12:34:56.789'), ('c2','alice','2026-10-29 00:00:00')`)
	got, err := d.Privates(ctx, a)
	want := []db.Private{{Nick: "bob", Connection: "c1", Time: "2026-10-29 12:34:56.789"}, {Nick: "newer", Connection: "c1", Time: "2026-10-30 01:00:00"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("alice privates = %v, %v", got, err)
	}
	if got, err := d.Privates(ctx, c); err != nil || got == nil || len(got) != 0 {
		t.Errorf("carol privates = %#v, %v", got, err)
	}
}

func TestSeen(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a, c := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "carol")
	testenv.AddConnection(t, d, "c1", a, "{}")
	exec(t, d, `INSERT INTO seen ("user", connection, channel, message_id) VALUES ($1,'c1','#a',7), ($1,'gone','#a',9)`, a)
	got, err := d.Seen(ctx, a)
	if err != nil || !reflect.DeepEqual(got, []db.SeenRow{{Connection: "c1", Channel: "#a", MessageID: 7}}) {
		t.Errorf("alice seen = %v, %v", got, err)
	}
	if got, err := d.Seen(ctx, c); err != nil || got == nil || len(got) != 0 {
		t.Errorf("carol seen = %#v, %v", got, err)
	}
}

func TestChannelNames(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	testenv.AddConnection(t, d, "c2", a, "{}")
	logRow(t, d, 1, "c1", a, "#a", "PRIVMSG", false)
	logRow(t, d, 3, "c1", a, "#b", "PRIVMSG", false)
	logRow(t, d, 5, "c1", a, "#a", "PRIVMSG", false)
	got, err := d.ChannelNames(ctx, "c1")
	if err != nil || !reflect.DeepEqual(got, []string{"#a", "#b"}) {
		t.Errorf("c1 channels = %v, %v", got, err)
	}
	if got, err := d.ChannelNames(ctx, "c2"); err != nil || got == nil || len(got) != 0 {
		t.Errorf("c2 channels = %#v, %v", got, err)
	}
}

func TestMissed(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a, b := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob")
	testenv.AddConnection(t, d, "c1", a, "{}")
	testenv.AddConnection(t, d, "bc", b, "{}")
	logRow(t, d, 1, "c1", a, "#a", "PRIVMSG", false)
	logRow(t, d, 2, "c1", a, "#a", "PRIVMSG", true)
	logRow(t, d, 3, "c1", a, "#a", "JOIN", false)
	logRow(t, d, 4, "c1", a, "#a", nil, false)
	logRow(t, d, 5, "c1", a, "#quiet", "PRIVMSG", false)
	logRow(t, d, 6, "bc", b, "#a", "PRIVMSG", false)

	got, err := d.Missed(ctx, a, []db.MissedEntry{
		{Connection: "c1", Channel: "#a", After: 1},
		{Connection: "c1", Channel: "#quiet", After: 5},
		{Connection: "bc", Channel: "#a", After: 0},
		{Connection: "nope", Channel: "#a", After: 0},
	})
	want := map[string]map[string]db.MissedCounts{"c1": {"#a": {Messages: 1, Events: 1, Highlights: 1}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("missed = %v, %v", got, err)
	}
	if got, err := d.Missed(ctx, a, nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("missed nil = %#v, %v", got, err)
	}
}
