package db_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

func userIDs(rows []db.UserRow) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// seedUsers makes never (last_login NULL), old, recent, in that login order,
// plus tie, which shares old's last_login.
func seedUsers(t *testing.T, d *db.DB) (never, old, recent, tie string) {
	t.Helper()
	never = testenv.AddUser(t, d, "never")
	old = testenv.AddUser(t, d, "old")
	recent = testenv.AddUser(t, d, "recent")
	tie = testenv.AddUser(t, d, "tie")
	exec(t, d, `ALTER TABLE "user" ALTER COLUMN last_login DROP NOT NULL`)
	exec(t, d, `UPDATE "user" SET last_login = NULL WHERE id = '`+never+`'`)
	exec(t, d, `UPDATE "user" SET last_login = '2020-01-02 03:04:05' WHERE id IN ('`+old+`','`+tie+`')`)
	exec(t, d, `UPDATE "user" SET last_login = now() at time zone 'utc' WHERE id = '`+recent+`'`)
	return
}

func TestListUsers(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	never, old, recent, tie := seedUsers(t, d)
	testenv.AddConnection(t, d, "c1", old, `{}`)
	testenv.AddConnection(t, d, "c2", old, `{}`)
	exec(t, d, `UPDATE "user" SET password_hash = 'h' WHERE id = '`+old+`'`)
	exec(t, d, `UPDATE "user" SET registered = '2019-05-06 07:08:09', verified = NULL WHERE id = '`+old+`'`)

	all, err := d.ListUsers(ctx, db.UserFilter{})
	if err != nil {
		t.Fatal(err)
	}
	second, third := old, tie
	if tie < old {
		second, third = tie, old
	}
	if want := []string{never, second, third, recent}; !reflect.DeepEqual(userIDs(all), want) {
		t.Fatalf("order = %v, want %v", userIDs(all), want)
	}
	var o db.UserRow
	for _, r := range all {
		if r.ID == old {
			o = r
		}
	}
	if o.Username != "old" || o.Email != "old@example.com" || o.Verified != nil || o.Connections != 2 ||
		o.Password != "bcrypt" || o.LastLogin == nil || !o.LastLogin.Equal(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)) ||
		o.Registered == nil || !o.Registered.Equal(time.Date(2019, 5, 6, 7, 8, 9, 0, time.UTC)) {
		t.Errorf("row = %+v", o)
	}
	if all[0].LastLogin != nil || all[0].Password != "none" {
		t.Errorf("never = %+v", all[0])
	}

	cutoff := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err := d.ListUsers(ctx, db.UserFilter{InactiveSince: &cutoff})
	if err != nil || !reflect.DeepEqual(userIDs(got), []string{never, second, third}) {
		t.Errorf("inactive = %v, %v", userIDs(got), err)
	}

	got, err = d.ListUsers(ctx, db.UserFilter{IDs: []string{recent, "nosuchid", never}})
	if err != nil || !reflect.DeepEqual(userIDs(got), []string{never, recent}) {
		t.Errorf("ids = %v, %v", userIDs(got), err)
	}
	got, err = d.ListUsers(ctx, db.UserFilter{IDs: []string{}})
	if err != nil || len(got) != 0 {
		t.Errorf("empty ids = %v, %v", userIDs(got), err)
	}
}

func TestListUsersPasswordNone(t *testing.T) {
	d := testenv.Postgres(t)
	a := testenv.AddUser(t, d, "a")
	exec(t, d, `UPDATE "user" SET password_hash = ''`)
	got, err := d.ListUsers(context.Background(), db.UserFilter{IDs: []string{a}})
	if err != nil || len(got) != 1 || got[0].Password != "none" {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestLookupUsers(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "Alice")
	b := testenv.AddUser(t, d, "bob")
	// user c's username is user b's email, differing in case
	c := testenv.AddUser(t, d, "Bob_x")
	// old accounts can have such names; the signup rule is a later CHECK
	exec(t, d, `ALTER TABLE "user" DROP CONSTRAINT user_username_valid`)
	exec(t, d, `UPDATE "user" SET username = 'BOB@example.com' WHERE id = '`+c+`'`)
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{a, []string{a}},
		{"alice", []string{a}},
		{"ALICE@example.com", []string{a}},
		{"bob@example.com", []string{b, c}},
		{"nobody", []string{}},
	} {
		got, err := d.LookupUsers(ctx, tc.q)
		if err != nil {
			t.Fatal(err)
		}
		g := userIDs(got)
		if len(g) == 2 && g[0] > g[1] {
			g[0], g[1] = g[1], g[0]
		}
		w := append([]string{}, tc.want...)
		if len(w) == 2 && w[0] > w[1] {
			w[0], w[1] = w[1], w[0]
		}
		if !reflect.DeepEqual(g, w) {
			t.Errorf("lookup %q = %v, want %v", tc.q, g, w)
		}
	}
}

func seedCounts(t *testing.T, d *db.DB) (user, conn string) {
	t.Helper()
	user = testenv.AddUser(t, d, "owner")
	other := testenv.AddUser(t, d, "other")
	conn = "conn1"
	testenv.AddConnection(t, d, conn, user, `{}`)
	testenv.AddConnection(t, d, "conn2", user, `{}`)
	testenv.AddConnection(t, d, "otherc", other, `{}`)
	testenv.AddLog(t, d, 1, conn, user)
	testenv.AddLog(t, d, 2, conn, user)
	testenv.AddLog(t, d, 3, "gone", user) // the user's, connection already deleted
	testenv.AddLog(t, d, 4, "otherc", other)
	exec(t, d, `INSERT INTO log_archive (connection, "user", channel, first_id, last_id, min_time, max_time, count, messages)
	  VALUES ('conn1', '`+user+`', '#a', 1, 1, now(), now(), 1, '[]')`)
	exec(t, d, `INSERT INTO seen ("user", connection, channel, message_id) VALUES ('`+user+`', 'conn1', '#a', 1), ('`+user+`', 'conn2', '#a', 1), ('`+other+`', 'otherc', '#a', 1)`)
	exec(t, d, `INSERT INTO private (connection, nick, time) VALUES ('conn1', 'n', now()), ('otherc', 'n', now())`)
	exec(t, d, `INSERT INTO pref ("user", name, value) VALUES ('`+user+`', 'p', 'v')`)
	exec(t, d, `INSERT INTO web_push (endpoint, key, auth, "user") VALUES ('e', 'k', 'a', '`+user+`')`)
	exec(t, d, `INSERT INTO login (token_hash, "user") VALUES ('\x01', '`+user+`'), ('\x02', '`+user+`')`)
	return
}

func TestUserCounts(t *testing.T) {
	d := testenv.Postgres(t)
	user, _ := seedCounts(t, d)
	got, err := d.UserCounts(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"log": 3, "log_archive": 1, "seen": 2, "private": 1, "pref": 1, "web_push": 1, "connection": 2, "login": 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
}

func TestConnectionCounts(t *testing.T) {
	d := testenv.Postgres(t)
	_, conn := seedCounts(t, d)
	got, err := d.ConnectionCounts(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"log": 2, "log_archive": 1, "seen": 1, "private": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
}

func cids(rows []db.ConnectionRow) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func TestListConnections(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	never, old, recent, _ := seedUsers(t, d)
	testenv.AddConnection(t, d, "r1", recent, `{"Host":"r.example","Port":6667}`)
	testenv.AddConnection(t, d, "o2", old, `{"Host":"o.example","Port":6697}`)
	testenv.AddConnection(t, d, "o1", old, `{}`)
	testenv.AddConnection(t, d, "n1", never, `{}`)
	testenv.AddConnection(t, d, "orphan", "nouser", `{}`)
	exec(t, d, `UPDATE connection SET liercd = 'next' WHERE id IN ('o2', 'r1')`)
	exec(t, d, `UPDATE connection SET liercd = '' WHERE id = 'o1'`)

	all, err := d.ListConnections(ctx, db.ConnectionFilter{})
	if err != nil {
		t.Fatal(err)
	}
	// owner last_login nulls first (never's and the owner-less row, by user id), then oldest, then recent
	first := map[string]bool{}
	if len(all) == 5 {
		first[all[0].ID], first[all[1].ID] = true, true
	}
	if got := cids(all); len(got) != 5 || !first["n1"] || !first["orphan"] || got[2] != "o1" || got[3] != "o2" || got[4] != "r1" {
		t.Fatalf("order = %v", got)
	}
	for _, r := range all {
		switch r.ID {
		case "o2":
			if r.User != old || r.Username != "old" || r.Instance != "next" || string(r.Config) != `{"Host": "o.example", "Port": 6697}` {
				t.Errorf("o2 = %+v (%s)", r, r.Config)
			}
		case "o1":
			if r.Instance != "main" {
				t.Errorf("o1 instance = %q", r.Instance)
			}
		case "n1":
			if r.Instance != "main" {
				t.Errorf("n1 instance = %q", r.Instance)
			}
		case "orphan":
			if r.Username != "" {
				t.Errorf("orphan username = %q", r.Username)
			}
		}
	}

	got, err := d.ListConnections(ctx, db.ConnectionFilter{Instance: "next"})
	if err != nil || !reflect.DeepEqual(cids(got), []string{"o2", "r1"}) {
		t.Errorf("next = %v, %v", cids(got), err)
	}
	got, err = d.ListConnections(ctx, db.ConnectionFilter{Instance: "main"})
	if err != nil || len(got) != 3 {
		t.Errorf("main = %v, %v", cids(got), err)
	}
	cutoff := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err = d.ListConnections(ctx, db.ConnectionFilter{InactiveSince: &cutoff})
	// the owner-less connection isn't anyone's inactive one
	if err != nil || !reflect.DeepEqual(cids(got), []string{"n1", "o1", "o2"}) {
		t.Errorf("inactive = %v, %v", cids(got), err)
	}
	got, err = d.ListConnections(ctx, db.ConnectionFilter{ID: "r1"})
	if err != nil || !reflect.DeepEqual(cids(got), []string{"r1"}) {
		t.Errorf("id = %v, %v", cids(got), err)
	}
}

func TestLastLogID(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	if n, err := d.LastLogID(ctx); err != nil || n != 0 {
		t.Fatalf("empty = %d, %v", n, err)
	}
	u := testenv.AddUser(t, d, "u")
	testenv.AddConnection(t, d, "c", u, `{}`)
	testenv.AddLog(t, d, 7, "c", u)
	testenv.AddLog(t, d, 5, "c", u)
	if n, err := d.LastLogID(ctx); err != nil || n != 7 {
		t.Errorf("got %d, %v", n, err)
	}
}

func TestStaleSeenCount(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	u := testenv.AddUser(t, d, "u")
	testenv.AddConnection(t, d, "c", u, `{}`)
	exec(t, d, `INSERT INTO seen ("user", connection, channel, message_id) VALUES ('`+u+`', 'c', '#fresh', 1), ('`+u+`', 'gone', '#orphan', 1)`)
	exec(t, d, `INSERT INTO seen ("user", connection, channel, message_id, updated) VALUES ('`+u+`', 'c', '#old', 1, now() - interval '2 months')`)
	if n, err := d.StaleSeenCount(ctx); err != nil || n != 2 {
		t.Errorf("got %d, %v", n, err)
	}
}

func TestDeleteUserData(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	user, _ := seedCounts(t, d)
	want, _ := d.UserCounts(ctx, user)
	changes, counts, err := d.DeleteUserData(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(counts, want) {
		t.Errorf("counts = %v, want %v", counts, want)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %v", changes)
	}
	for _, c := range changes {
		if c.User != user || c.Action != "delete" || (c.Connection != "conn1" && c.Connection != "conn2") {
			t.Errorf("change %+v", c)
		}
	}
	got, _ := d.UserCounts(ctx, user)
	for k, v := range got {
		if v != 0 {
			t.Errorf("%s = %d after delete", k, v)
		}
	}
	if _, _, err := d.DeleteUserData(ctx, user); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("again: %v, want ErrNotFound", err)
	}
}

func TestDeleteConnectionData(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	user, conn := seedCounts(t, d)
	changes, counts, err := d.DeleteConnectionData(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"log": 2, "log_archive": 1, "seen": 1, "private": 1, "connection": 1}
	if !reflect.DeepEqual(counts, want) {
		t.Errorf("counts = %v, want %v", counts, want)
	}
	if !reflect.DeepEqual(changes, []db.ConnectionChange{{User: user, Connection: conn, Action: "delete"}}) {
		t.Errorf("changes = %v", changes)
	}
	if _, _, err := d.DeleteConnectionData(ctx, conn); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("again: %v, want ErrNotFound", err)
	}
}

func TestPruneStaleSeen(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	u := testenv.AddUser(t, d, "u")
	testenv.AddConnection(t, d, "c", u, `{}`)
	exec(t, d, `INSERT INTO seen ("user", connection, channel, message_id) VALUES ('`+u+`', 'c', '#fresh', 1), ('`+u+`', 'gone', '#orphan', 1)`)
	exec(t, d, `INSERT INTO seen ("user", connection, channel, message_id, updated) VALUES ('`+u+`', 'c', '#old', 1, now() - interval '2 months')`)
	if n, err := d.PruneStaleSeen(ctx); err != nil || n != 2 {
		t.Errorf("got %d, %v", n, err)
	}
	if n, _ := d.StaleSeenCount(ctx); n != 0 {
		t.Errorf("stale left: %d", n)
	}
}

func TestSweepLateLogs(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	u := testenv.AddUser(t, d, "u")
	o := testenv.AddUser(t, d, "o")
	testenv.AddLog(t, d, 5, "c1", u)    // not late
	testenv.AddLog(t, d, 11, "c1", u)   // late
	testenv.AddLog(t, d, 12, "c2", u)   // late, the user's other connection
	testenv.AddLog(t, d, 13, "gone", u) // late, of a connection deleted before
	testenv.AddLog(t, d, 14, "oc", o)   // late, someone else's
	exec(t, d, `INSERT INTO log_archive (connection, "user", channel, first_id, last_id, min_time, max_time, count, messages)
	  VALUES ('c1', '`+u+`', '#a', 1, 1, now(), now(), 1, '[]'), ('oc', '`+o+`', '#a', 1, 1, now(), now(), 1, '[]')`)
	remaining := func() []int64 {
		rows, err := d.Pool.Query(ctx, `SELECT id FROM log ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []int64
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		return ids
	}

	// a connection sweep: only c1's late rows
	n, blocks, err := d.SweepLateLogs(ctx, "", []string{"c1"}, 10)
	if err != nil || n != 1 || blocks != 1 {
		t.Fatalf("connection sweep = %d rows, %d blocks, %v; want 1, 1", n, blocks, err)
	}
	if got := remaining(); !reflect.DeepEqual(got, []int64{5, 12, 13, 14}) {
		t.Errorf("after connection sweep: %v", got)
	}
	// a user sweep: late rows of the user, including of a deleted connection
	n, blocks, err = d.SweepLateLogs(ctx, u, []string{"c1", "c2"}, 10)
	if err != nil || n != 2 || blocks != 0 {
		t.Fatalf("user sweep = %d rows, %d blocks, %v; want 2, 0", n, blocks, err)
	}
	if got := remaining(); !reflect.DeepEqual(got, []int64{5, 14}) {
		t.Errorf("after user sweep: %v", got)
	}
	var archived int
	d.Pool.QueryRow(ctx, `SELECT count(*) FROM log_archive`).Scan(&archived)
	if archived != 1 {
		t.Errorf("log_archive rows = %d, want only the other user's", archived)
	}
	// after 0 sweeps everything of the connection
	if n, blocks, err := d.SweepLateLogs(ctx, "", []string{"oc"}, 0); err != nil || n != 1 || blocks != 1 {
		t.Errorf("after 0 = %d rows, %d blocks, %v; want 1, 1", n, blocks, err)
	}
}
