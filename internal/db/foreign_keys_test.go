package db_test

import (
	"fmt"
	"os"
	"testing"
	"testing/fstest"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

// migrationsUpTo is the embedded migrations numbered up to last.
func migrationsUpTo(t *testing.T, last int) fstest.MapFS {
	t.Helper()
	fs := fstest.MapFS{}
	for _, m := range embeddedMigrations(t) {
		if m.version > last {
			continue
		}
		name := fmt.Sprintf("%04d_%s.sql", m.version, m.name)
		b, err := os.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		fs[name] = &fstest.MapFile{Data: b}
	}
	return fs
}

func execAll(t *testing.T, d *db.DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := d.Pool.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// seedUser adds user u with a row in each of its own tables.
func seedUser(t *testing.T, d *db.DB, u string) {
	t.Helper()
	execAll(t, d,
		fmt.Sprintf(`INSERT INTO users (id, email, username) VALUES ('%[1]s', '%[1]s@example.com', '%[1]s')`, u),
		fmt.Sprintf(`INSERT INTO prefs (user_id, name, value) VALUES ('%s', 'p', 'v')`, u),
		fmt.Sprintf(`INSERT INTO web_push_subscriptions (endpoint, key, auth, user_id) VALUES ('https://push/%[1]s', 'k', 'a', '%[1]s')`, u),
		fmt.Sprintf(`INSERT INTO logins (token_hash, user_id) VALUES ('\x%x', '%s')`, u, u),
	)
}

func seedConn(t *testing.T, d *db.DB, u, c string) {
	t.Helper()
	execAll(t, d, fmt.Sprintf(`INSERT INTO connections (id, user_id, config) VALUES ('%s', '%s', '{}')`, c, u))
	execAll(t, d, connRows(u, c)...)
}

// connRows are a connection's history rows, for owners that may not exist.
func connRows(u, c string) []string {
	return []string{
		fmt.Sprintf(`INSERT INTO logs (connection_id, user_id, channel, time, message) VALUES ('%s', '%s', '#a', now(), '{}')`, c, u),
		fmt.Sprintf(`INSERT INTO log_archives (connection_id, user_id, channel, first_id, last_id, min_time, max_time, count, messages)
			VALUES ('%s', '%s', '#a', 1, 2, now(), now(), 2, '[]')`, c, u),
		fmt.Sprintf(`INSERT INTO seen_markers (user_id, connection_id, channel, message_id) VALUES ('%s', '%s', '#a', 1)`, u, c),
		fmt.Sprintf(`INSERT INTO private_chats (connection_id, nick, time) VALUES ('%s', 'bob', now())`, c),
	}
}

var ownedTables = []string{"connections", "logs", "log_archives", "seen_markers", "private_chats", "prefs", "web_push_subscriptions", "logins"}

// rowsOf counts, per table, the rows belonging to user u or connection c.
func rowsOf(t *testing.T, d *db.DB, u, c string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range ownedTables {
		var where string
		switch table {
		case "connections":
			where = fmt.Sprintf("user_id = '%s' OR id = '%s'", u, c)
		case "private_chats":
			where = fmt.Sprintf("connection_id = '%s'", c)
		case "prefs", "web_push_subscriptions", "logins":
			where = fmt.Sprintf("user_id = '%s'", u)
		default:
			where = fmt.Sprintf("user_id = '%s' OR connection_id = '%s'", u, c)
		}
		var n int
		if err := d.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE "+where).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

func total(m map[string]int) (n int) {
	for _, v := range m {
		n += v
	}
	return n
}

// Migration 3 deletes the rows whose user or connection is gone, and only
// those, before it adds the keys.
func TestMigration3RemovesOrphans(t *testing.T) {
	d := testenv.EmptyPostgres(t)
	if err := d.Migrate(ctx, db.MigrateOptions{FS: migrationsUpTo(t, 2)}); err != nil {
		t.Fatal(err)
	}
	seedUser(t, d, "u1")
	seedConn(t, d, "u1", "c1")
	// a connection whose user is gone, with its history
	execAll(t, d, `INSERT INTO connections (id, user_id, config) VALUES ('cx', 'gone_user', '{}')`)
	execAll(t, d, connRows("gone_user", "cx")...)
	// history of a connection that's gone, of a live user and of a gone one
	execAll(t, d, connRows("u1", "gone_conn")...)
	execAll(t, d, connRows("gone_user2", "gone_conn2")...)
	execAll(t, d,
		`INSERT INTO prefs (user_id, name, value) VALUES ('gone_user', 'p', 'v')`,
		`INSERT INTO web_push_subscriptions (endpoint, key, auth, user_id) VALUES ('https://push/gone', 'k', 'a', 'gone_user')`,
	)
	live := rowsOf(t, d, "u1", "c1")

	if err := d.Migrate(ctx, db.MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, o := range [][2]string{{"gone_user", "cx"}, {"gone_user2", "gone_conn2"}, {"nobody", "gone_conn"}} {
		if left := rowsOf(t, d, o[0], o[1]); total(left) != 0 {
			t.Errorf("rows of %s/%s left: %v", o[0], o[1], left)
		}
	}
	got := rowsOf(t, d, "u1", "c1")
	// u1's rows of gone_conn (logs, archive, seen) went; the rest of u1's stay
	want := map[string]int{"connections": 1, "logs": 1, "log_archives": 1, "seen_markers": 1, "private_chats": 1, "prefs": 1, "web_push_subscriptions": 1, "logins": 1}
	for table, n := range want {
		if got[table] != n {
			t.Errorf("%s: %d of u1/c1's rows left, want %d (before: %d)", table, got[table], n, live[table])
		}
	}
}

// Deleting a connection takes exactly its rows; deleting a user takes
// everything of theirs.
func TestForeignKeysCascade(t *testing.T) {
	d := testenv.Postgres(t)
	seedUser(t, d, "u1")
	seedConn(t, d, "u1", "c1")
	seedConn(t, d, "u1", "c2")
	seedUser(t, d, "u2")
	seedConn(t, d, "u2", "c3")

	execAll(t, d, `DELETE FROM connections WHERE id = 'c1'`)
	if left := rowsOf(t, d, "nobody", "c1"); total(left) != 0 {
		t.Errorf("c1's rows left: %v", left)
	}
	if got := rowsOf(t, d, "nobody", "c2"); got["logs"] != 1 || got["private_chats"] != 1 {
		t.Errorf("c2's rows: %v", got)
	}

	execAll(t, d, `DELETE FROM users WHERE id = 'u1'`)
	if left := rowsOf(t, d, "u1", "c2"); total(left) != 0 {
		t.Errorf("u1's rows left: %v", left)
	}
	want := map[string]int{"connections": 1, "logs": 1, "log_archives": 1, "seen_markers": 1, "private_chats": 1, "prefs": 1, "web_push_subscriptions": 1, "logins": 1}
	got := rowsOf(t, d, "u2", "c3")
	for table, n := range want {
		if got[table] != n {
			t.Errorf("u2 %s: %d, want %d", table, got[table], n)
		}
	}
}

// Every owner column has a cascading key to its owner, and an index that
// leads with it, so a cascade doesn't scan the table.
func TestOwnerColumnsHaveKeysAndIndexes(t *testing.T) {
	d := testenv.Postgres(t)
	for _, k := range []struct{ table, column, owner string }{
		{"connections", "user_id", "users"},
		{"logs", "connection_id", "connections"}, {"logs", "user_id", "users"},
		{"log_archives", "connection_id", "connections"}, {"log_archives", "user_id", "users"},
		{"seen_markers", "connection_id", "connections"}, {"seen_markers", "user_id", "users"},
		{"private_chats", "connection_id", "connections"},
		{"prefs", "user_id", "users"},
		{"web_push_subscriptions", "user_id", "users"},
		{"logins", "user_id", "users"},
	} {
		var keys, indexes int
		err := d.Pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM pg_constraint c
			         WHERE c.contype = 'f' AND c.conrelid = $1::regclass AND c.confrelid = $3::regclass
			           AND c.confdeltype = 'c'
			           AND c.conkey = ARRAY[(SELECT attnum FROM pg_attribute WHERE attrelid = $1::regclass AND attname = $2)]),
			       (SELECT count(*) FROM pg_index i
			         WHERE i.indrelid = $1::regclass
			           AND i.indkey[0] = (SELECT attnum FROM pg_attribute WHERE attrelid = $1::regclass AND attname = $2))`,
			k.table, k.column, k.owner).Scan(&keys, &indexes)
		if err != nil {
			t.Fatal(err)
		}
		if keys != 1 {
			t.Errorf("%s.%s: %d cascading keys to %s, want 1", k.table, k.column, keys, k.owner)
		}
		if indexes == 0 {
			t.Errorf("%s.%s: no index leads with it", k.table, k.column)
		}
	}
}
