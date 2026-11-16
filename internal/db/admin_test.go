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
	exec(t, d, `ALTER TABLE users ALTER COLUMN last_login DROP NOT NULL`)
	exec(t, d, `UPDATE users SET last_login = NULL WHERE id = '`+never+`'`)
	exec(t, d, `UPDATE users SET last_login = '2020-01-02 03:04:05' WHERE id IN ('`+old+`','`+tie+`')`)
	exec(t, d, `UPDATE users SET last_login = now() at time zone 'utc' WHERE id = '`+recent+`'`)
	return
}

func TestListUsers(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	never, old, recent, tie := seedUsers(t, d)
	testenv.AddConnection(t, d, "c1", old, `{}`)
	testenv.AddConnection(t, d, "c2", old, `{}`)
	exec(t, d, `UPDATE users SET password_hash = 'h' WHERE id = '`+old+`'`)
	exec(t, d, `UPDATE users SET registered = '2019-05-06 07:08:09', verified = NULL WHERE id = '`+old+`'`)

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
	exec(t, d, `UPDATE users SET password_hash = ''`)
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
	exec(t, d, `ALTER TABLE users DROP CONSTRAINT users_username_valid`)
	exec(t, d, `UPDATE users SET username = 'BOB@example.com' WHERE id = '`+c+`'`)
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
	testenv.AddLog(t, d, 4, "otherc", other)
	exec(t, d, `INSERT INTO log_archives (connection_id, user_id, channel, first_id, last_id, min_time, max_time, count, messages)
	  VALUES ('conn1', '`+user+`', '#a', 1, 1, now(), now(), 1, '[]')`)
	exec(t, d, `INSERT INTO seen_markers (user_id, connection_id, channel, message_id) VALUES ('`+user+`', 'conn1', '#a', 1), ('`+user+`', 'conn2', '#a', 1), ('`+other+`', 'otherc', '#a', 1)`)
	exec(t, d, `INSERT INTO private_chats (connection_id, nick, time) VALUES ('conn1', 'n', now()), ('otherc', 'n', now())`)
	exec(t, d, `INSERT INTO prefs (user_id, name, value) VALUES ('`+user+`', 'p', 'v')`)
	exec(t, d, `INSERT INTO web_push_subscriptions (endpoint, key, auth, user_id) VALUES ('e', 'k', 'a', '`+user+`')`)
	exec(t, d, `INSERT INTO logins (token_hash, user_id) VALUES ('\x01', '`+user+`'), ('\x02', '`+user+`')`)
	return
}

func TestUserCounts(t *testing.T) {
	d := testenv.Postgres(t)
	user, _ := seedCounts(t, d)
	got, err := d.UserCounts(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"log": 2, "log_archive": 1, "seen": 2, "private": 1, "pref": 1, "web_push": 1, "connection": 2, "login": 2}
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
	exec(t, d, `UPDATE connections SET liercd = 'next' WHERE id IN ('o2', 'r1')`)
	exec(t, d, `UPDATE connections SET liercd = 'main' WHERE id = 'o1'`)

	all, err := d.ListConnections(ctx, db.ConnectionFilter{})
	if err != nil {
		t.Fatal(err)
	}
	// owner last_login nulls first, then oldest, then recent
	if got := cids(all); !reflect.DeepEqual(got, []string{"n1", "o1", "o2", "r1"}) {
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
		}
	}

	got, err := d.ListConnections(ctx, db.ConnectionFilter{Instance: "next"})
	if err != nil || !reflect.DeepEqual(cids(got), []string{"o2", "r1"}) {
		t.Errorf("next = %v, %v", cids(got), err)
	}
	got, err = d.ListConnections(ctx, db.ConnectionFilter{Instance: "main"})
	if err != nil || !reflect.DeepEqual(cids(got), []string{"n1", "o1"}) {
		t.Errorf("main = %v, %v", cids(got), err)
	}
	cutoff := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err = d.ListConnections(ctx, db.ConnectionFilter{InactiveSince: &cutoff})
	if err != nil || !reflect.DeepEqual(cids(got), []string{"n1", "o1", "o2"}) {
		t.Errorf("inactive = %v, %v", cids(got), err)
	}
	got, err = d.ListConnections(ctx, db.ConnectionFilter{ID: "r1"})
	if err != nil || !reflect.DeepEqual(cids(got), []string{"r1"}) {
		t.Errorf("id = %v, %v", cids(got), err)
	}
	got, err = d.ListConnections(ctx, db.ConnectionFilter{User: old})
	if err != nil || !reflect.DeepEqual(cids(got), []string{"o1", "o2"}) {
		t.Errorf("user = %v, %v", cids(got), err)
	}
	got, err = d.ListConnections(ctx, db.ConnectionFilter{User: old, Instance: "next"})
	if err != nil || !reflect.DeepEqual(cids(got), []string{"o2"}) {
		t.Errorf("user and instance = %v, %v", cids(got), err)
	}
}

func TestStaleSeenCount(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	u := testenv.AddUser(t, d, "u")
	testenv.AddConnection(t, d, "c", u, `{}`)
	exec(t, d, `INSERT INTO seen_markers (user_id, connection_id, channel, message_id) VALUES ('`+u+`', 'c', '#fresh', 1)`)
	exec(t, d, `INSERT INTO seen_markers (user_id, connection_id, channel, message_id, updated) VALUES ('`+u+`', 'c', '#old', 1, now() - interval '2 months')`)
	if n, err := d.StaleSeenCount(ctx); err != nil || n != 1 {
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
	exec(t, d, `INSERT INTO seen_markers (user_id, connection_id, channel, message_id) VALUES ('`+u+`', 'c', '#fresh', 1)`)
	exec(t, d, `INSERT INTO seen_markers (user_id, connection_id, channel, message_id, updated) VALUES ('`+u+`', 'c', '#old', 1, now() - interval '2 months')`)
	if n, err := d.PruneStaleSeen(ctx); err != nil || n != 1 {
		t.Errorf("got %d, %v", n, err)
	}
	if n, _ := d.StaleSeenCount(ctx); n != 0 {
		t.Errorf("stale left: %d", n)
	}
}

// rowsLeft counts the rows in every owned table matching user or conn.
func rowsLeft(t *testing.T, d *db.DB, user, conn string) int {
	t.Helper()
	var n int
	err := d.Pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM connections WHERE id = $2 OR user_id = $1) +
		(SELECT count(*) FROM logs WHERE connection_id = $2 OR user_id = $1) +
		(SELECT count(*) FROM log_archives WHERE connection_id = $2 OR user_id = $1) +
		(SELECT count(*) FROM seen_markers WHERE connection_id = $2 OR user_id = $1) +
		(SELECT count(*) FROM private_chats WHERE connection_id = $2) +
		(SELECT count(*) FROM prefs WHERE user_id = $1) +
		(SELECT count(*) FROM web_push_subscriptions WHERE user_id = $1) +
		(SELECT count(*) FROM logins WHERE user_id = $1)`, user, conn).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The delete removes only the connection's row; the keys take the rest.
func TestDeleteConnectionDataCascades(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	user, conn := seedCounts(t, d)
	if _, _, err := d.DeleteConnectionData(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if n := rowsLeft(t, d, "nobody", conn); n != 0 {
		t.Errorf("%d rows of %s left", n, conn)
	}
	if got, _ := d.UserCounts(ctx, user); got["connection"] != 1 || got["pref"] != 1 {
		t.Errorf("the user's other rows: %v", got)
	}
}

func TestDeleteUserDataCascades(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	user, _ := seedCounts(t, d)
	if _, _, err := d.DeleteUserData(ctx, user); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"conn1", "conn2"} {
		if n := rowsLeft(t, d, user, c); n != 0 {
			t.Errorf("%d rows of %s/%s left", n, user, c)
		}
	}
	if n := rowsLeft(t, d, "nobody", "otherc"); n != 4 { // connection, log, seen, private
		t.Errorf("the other user's connection has %d rows, want 4", n)
	}
}

func TestPruneSeenKeepsRecentRows(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	u := testenv.AddUser(t, d, "u")
	testenv.AddConnection(t, d, "c", u, `{}`)
	exec(t, d, `INSERT INTO seen_markers (user_id, connection_id, channel, message_id, updated)
		VALUES ('`+u+`', 'c', '#fresh', 1, now() - interval '20 days'), ('`+u+`', 'c', '#old', 1, now() - interval '40 days')`)
	if n, err := d.PruneStaleSeen(ctx); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if got := one[string](t, d, `SELECT string_agg(channel, ',') FROM seen_markers`); got != "#fresh" {
		t.Errorf("left %s", got)
	}
}
