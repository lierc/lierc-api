package db_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestNotifyTarget(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// u1 confirmed, email on, two push subscriptions; u2 email off; u4
	// from before confirmation (verified NULL); u5 not confirmed
	for _, c := range []string{"c1", "c2", "c4", "c5"} {
		testenv.EnsureConnection(t, d, c, "u"+c[1:])
	}
	exec(`UPDATE users SET verified = NULL WHERE id = 'u4'`)
	exec(`UPDATE users SET verified = false WHERE id = 'u5'`)
	for _, u := range []string{"u1", "u4", "u5"} {
		exec(`INSERT INTO prefs (user_id, name, value) VALUES ($1, 'email', 'true')`, u)
	}
	exec(`INSERT INTO prefs (user_id, name, value) VALUES ('u2', 'email', 'false')`)
	exec(`INSERT INTO web_push_subscriptions (endpoint, key, auth, user_id) VALUES
		('https://push.example/b', 'kb', 'ab', 'u1'), ('https://push.example/a', 'ka', 'aa', 'u1')`)

	tg, ok, err := d.NotifyTarget(ctx, "c1")
	email := one[string](t, d, `SELECT email FROM users WHERE id = 'u1'`)
	if err != nil || !ok || tg.User != "u1" || tg.Email != email || !tg.EmailOn || len(tg.WebPush) != 2 ||
		tg.WebPush[0] != (db.WebPushSubscription{Endpoint: "https://push.example/a", Key: "ka", Auth: "aa"}) {
		t.Errorf("c1: %+v %v %v", tg, ok, err)
	}
	if tg, ok, err := d.NotifyTarget(ctx, "c2"); err != nil || !ok || tg.EmailOn || len(tg.WebPush) != 0 {
		t.Errorf("c2: %+v %v %v", tg, ok, err)
	}
	if tg, _, err := d.NotifyTarget(ctx, "c4"); err != nil || !tg.EmailOn {
		t.Errorf("c4 (verified NULL): %+v %v", tg, err)
	}
	if tg, _, err := d.NotifyTarget(ctx, "c5"); err != nil || tg.EmailOn {
		t.Errorf("c5 (unconfirmed): %+v %v", tg, err)
	}
	if _, ok, err := d.NotifyTarget(ctx, "nope"); ok || err != nil {
		t.Errorf("nope: %v %v", ok, err)
	}
}

func TestHighlightWords(t *testing.T) {
	d := testenv.Postgres(t)
	testenv.AddConnection(t, d, "c1", "u1", `{"Highlight": ["leedo"]}`)
	testenv.AddConnection(t, d, "c2", "u1", `{}`)
	w, err := d.HighlightWords(context.Background())
	if err != nil || len(w) != 1 || w["c1"] != `["leedo"]` {
		t.Errorf("HighlightWords = %v, %v", w, err)
	}
}

func TestSavePrivateAt(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	testenv.EnsureConnection(t, d, "c1", "u1")
	for _, at := range []float64{1700000000, 1700000100} {
		if err := d.SavePrivateAt(ctx, "c1", "bob", at); err != nil {
			t.Fatal(err)
		}
	}
	if got := one[int64](t, d, `SELECT extract(epoch FROM time)::bigint FROM private_chats WHERE connection_id = 'c1' AND nick = 'bob'`); got != 1700000100 {
		t.Errorf("time %d", got)
	}
	// no such connection: nothing stored, no error
	if err := d.SavePrivateAt(ctx, "nope", "bob", 1700000000); err != nil {
		t.Errorf("missing connection: %v", err)
	}
}

func TestInsertLogMissingConnection(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	e := db.LogEntry{Connection: "nope", Command: "PRIVMSG", Message: []byte(`{}`), Time: 1700000000}
	for _, channels := range [][]string{{"#a"}, {"#a", "#b"}} {
		if _, err := d.InsertLog(ctx, e, channels, false); !errors.Is(err, db.ErrGone) {
			t.Errorf("InsertLog %v: %v, want ErrGone", channels, err)
		}
	}
	if id, err := d.InsertLog(ctx, e, nil, false); id != 0 || err != nil {
		t.Errorf("InsertLog, no channels: %d %v", id, err)
	}
}

// listening starts ListenHighlights and returns how often changed was
// called, and the reported errors.
func listening(t *testing.T, d *db.DB, retry time.Duration) (*atomic.Int32, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	errs := make(chan error, 100)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.ListenHighlights(ctx, retry, func() { calls.Add(1) }, func(err error) { errs <- err })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("ListenHighlights didn't return after cancel")
		}
	})
	return &calls, errs
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The listener's backend, by the LISTEN it holds.
func listenerPID(t *testing.T, d *db.DB) int32 {
	t.Helper()
	var pid int32
	waitFor(t, "a listening backend", func() bool {
		err := d.Pool.QueryRow(context.Background(),
			`SELECT pid FROM pg_stat_activity WHERE datname = current_database() AND application_name = $1`, db.ListenerName).Scan(&pid)
		return err == nil
	})
	return pid
}

func TestListenHighlights(t *testing.T) {
	// a cleanup, not a defer: it must run after listening's, which stops
	// the listener
	t.Cleanup(db.SetListenPing(50 * time.Millisecond))
	d := testenv.Postgres(t)
	ctx := context.Background()
	calls, errs := listening(t, d, 10*time.Millisecond)

	// once on listening
	waitFor(t, "the first call", func() bool { return calls.Load() == 1 })
	pid := listenerPID(t, d)

	// pings in between keep the same connection, without calls
	time.Sleep(300 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Errorf("%d calls while idle, want 1", n)
	}
	if got := listenerPID(t, d); got != pid {
		t.Errorf("listener moved from backend %d to %d while idle", pid, got)
	}

	if err := d.NotifyHighlights(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the NOTIFY's call", func() bool { return calls.Load() == 2 })

	// a lost connection is reported, then it listens again, and calls
	// changed for the NOTIFYs it may have missed
	if _, err := d.Pool.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the call after reconnecting", func() bool { return calls.Load() == 3 })
	select {
	case err := <-errs:
		t.Logf("reported: %v", err)
	default:
		t.Error("lost connection not reported")
	}
	if got := listenerPID(t, d); got == pid {
		t.Error("still on the terminated backend")
	}
	if err := d.NotifyHighlights(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a NOTIFY after reconnecting", func() bool { return calls.Load() == 4 })
}

// NotifyLogged's payloads reach ListenStream's logged whole and in order;
// one too long for NOTIFY is refused before anything is sent.
func TestNotifyLogged(t *testing.T) {
	d := testenv.Postgres(t)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan string, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.ListenStream(ctx, 10*time.Millisecond, func(p []byte) { got <- string(p) }, func(err error) { t.Logf("reported: %v", err) })
	}()
	t.Cleanup(func() { cancel(); <-done })

	// probe until the LISTEN is in place, then drain stray probes
	waitFor(t, "the logged listener", func() bool {
		d.NotifyLogged(context.Background(), []byte("probe"))
		select {
		case <-got:
			return true
		case <-time.After(50 * time.Millisecond):
			return false
		}
	})
	for drained := false; !drained; {
		select {
		case <-got:
		case <-time.After(100 * time.Millisecond):
			drained = true
		}
	}

	want := []string{`{"ConnectionId":"c1","MessageId":1,"UserId":"u1"}`, `{"ConnectionId":"c1","MessageId":2,"UserId":"u1","Message":{"Params":["#a","é ünïcode"]}}`}
	for _, p := range want {
		if err := d.NotifyLogged(context.Background(), []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	for _, w := range want {
		select {
		case p := <-got:
			if p != w {
				t.Errorf("got %s, want %s", p, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("never got %s", w)
		}
	}

	big := []byte(`"` + strings.Repeat("x", db.MaxNotifyPayload) + `"`)
	if err := d.NotifyLogged(context.Background(), big); !errors.Is(err, db.ErrPayloadTooLong) {
		t.Errorf("long payload: %v, want ErrPayloadTooLong", err)
	}
	if err := d.NotifyLogged(context.Background(), big[:db.MaxNotifyPayload]); err != nil {
		t.Errorf("payload at the limit: %v", err)
	}
	select {
	case p := <-got:
		if len(p) != db.MaxNotifyPayload {
			t.Errorf("got %d bytes, want %d", len(p), db.MaxNotifyPayload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("never got the payload at the limit")
	}
}

// logMsg logs one line on conn under channels through InsertLog, as the
// logger does, and returns its id.
func logMsg(t *testing.T, d *db.DB, conn, command, message string, channels ...string) int64 {
	t.Helper()
	id, err := d.InsertLog(context.Background(), db.LogEntry{Connection: conn, Command: command, Message: []byte(message), Time: 1700000000}, channels, false)
	if err != nil {
		t.Fatalf("InsertLog %s: %v", message, err)
	}
	return id
}

// msgidRows is message_ids as "channel msgid log_id" lines, in order.
func msgidRows(t *testing.T, d *db.DB) []string {
	t.Helper()
	rows, err := d.Pool.Query(context.Background(), `SELECT c.connection_id || ' ' || c.channel || ' ' || m.msgid || ' ' || m.log_id
		FROM message_ids m JOIN channels c ON c.id = m.channel_id ORDER BY m.log_id, c.channel`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func count(t *testing.T, d *db.DB, table string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Each logged PRIVMSG or NOTICE with a msgid is mapped, under each channel
// it's logged in; other commands and lines without a msgid aren't, and a
// channel gets one channels row.
func TestInsertLogMessageIDs(t *testing.T) {
	d := testenv.Postgres(t)
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	id1 := logMsg(t, d, "c1", "PRIVMSG", `{"Command":"PRIVMSG","Params":["#a","hi"],"Tags":{"msgid":"m1"}}`, "#a")
	id2 := logMsg(t, d, "c1", "PRIVMSG", `{"Command":"PRIVMSG","Params":["#a","hi"],"Tags":{"msgid":"m2"}}`, "#a")
	id3 := logMsg(t, d, "c1", "NOTICE", `{"Command":"NOTICE","Params":["#a","hi"],"Tags":{"msgid":"n1"}}`, "#a")
	logMsg(t, d, "c1", "JOIN", `{"Command":"JOIN","Params":["#a"],"Tags":{"msgid":"j1"}}`, "#a")
	logMsg(t, d, "c1", "NICK", `{"Command":"NICK","Params":["new"],"Tags":{"msgid":"k1"}}`, "#a", "#b")
	logMsg(t, d, "c1", "PRIVMSG", `{"Command":"PRIVMSG","Params":["#a","no tags"]}`, "#a")
	// one line logged under two channels: a row for each logged row
	id4 := logMsg(t, d, "c1", "PRIVMSG", `{"Command":"PRIVMSG","Params":["#a","hi"],"Tags":{"msgid":"m3"}}`, "#a", "#b")
	want := []string{
		fmt.Sprintf("c1 #a m1 %d", id1),
		fmt.Sprintf("c1 #a m2 %d", id2),
		fmt.Sprintf("c1 #a n1 %d", id3),
		fmt.Sprintf("c1 #a m3 %d", id4-1),
		fmt.Sprintf("c1 #b m3 %d", id4),
	}
	if got := msgidRows(t, d); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("message_ids:\n%v\nwant:\n%v", got, want)
	}
	if n := count(t, d, "channels"); n != 2 {
		t.Errorf("%d channels rows, want 2 (#a, #b)", n)
	}
}

// Nothing is mapped for a connection that doesn't exist.
func TestInsertLogMessageIDsGoneConnection(t *testing.T) {
	d := testenv.Postgres(t)
	e := db.LogEntry{Connection: "nope", Command: "PRIVMSG", Message: []byte(`{"Command":"PRIVMSG","Params":["#a","hi"],"Tags":{"msgid":"m1"}}`), Time: 1700000000}
	if _, err := d.InsertLog(context.Background(), e, []string{"#a"}, false); !errors.Is(err, db.ErrGone) {
		t.Errorf("InsertLog: %v, want ErrGone", err)
	}
	for _, table := range []string{"channels", "message_ids", "replies"} {
		if n := count(t, d, table); n != 0 {
			t.Errorf("%d %s rows, want 0", n, table)
		}
	}
}

// The insert is one statement: when mapping fails, nothing is logged, so
// NSQ's retry doesn't leave half the rows behind.
func TestInsertLogAtomic(t *testing.T) {
	d := testenv.Postgres(t)
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	exec(t, d, `CREATE FUNCTION refuse() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$`)
	exec(t, d, `CREATE TRIGGER refuse BEFORE INSERT ON message_ids FOR EACH ROW EXECUTE FUNCTION refuse()`)
	e := db.LogEntry{Connection: "c1", Command: "PRIVMSG", Message: []byte(`{"Command":"PRIVMSG","Params":["#a","hi"],"Tags":{"msgid":"m1"}}`), Time: 1700000000}
	if _, err := d.InsertLog(context.Background(), e, []string{"#a"}, false); err == nil {
		t.Fatal("InsertLog succeeded with message_ids refusing rows")
	}
	if n := count(t, d, "logs"); n != 0 {
		t.Errorf("%d logs rows after the failed insert, want 0", n)
	}
}

// repliesRows is replies as "connection msgid reply_to thread_root" lines.
func repliesRows(t *testing.T, d *db.DB) map[string]string {
	t.Helper()
	rows, err := d.Pool.Query(context.Background(), `SELECT c.connection_id || ' ' || r.msgid, r.reply_to || ' ' || r.thread_root
		FROM replies r JOIN channels c ON c.id = r.channel_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		out[k] = v
	}
	return out
}

// Each reply is recorded with what it answers and its thread's root: the
// parent's root when the parent is a reply on the same connection, else
// the parent itself.
func TestInsertLogReplies(t *testing.T) {
	d := testenv.Postgres(t)
	a, b := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob")
	testenv.AddConnection(t, d, "c1", a, "{}")
	testenv.AddConnection(t, d, "c2", b, "{}")
	say := func(conn, msgid, tags string) {
		t.Helper()
		logMsg(t, d, conn, "PRIVMSG", fmt.Sprintf(`{"Command":"PRIVMSG","Params":["#a","x"],"Tags":{"msgid":%q%s}}`, msgid, tags), "#a")
	}
	say("c1", "o", "")
	say("c1", "r1", `,"+reply":"o"`)
	say("c1", "r2", `,"+reply":"r1"`)
	say("c1", "r3", `,"+draft/reply":"r2"`)
	say("c1", "r4", `,"+reply":"x","+draft/reply":"y"`)
	say("c1", "u1", `,"+reply":"unknown"`)
	say("c1", "e1", `,"+reply":""`)
	say("c1", "s1", `,"+reply":"s1"`)
	say("c2", "b1", `,"+reply":"r1"`)
	// a reaction carries +reply too, but it's a TAGMSG: no row
	logMsg(t, d, "c1", "TAGMSG", `{"Command":"TAGMSG","Params":["#a"],"Tags":{"msgid":"t1","+reply":"o","+draft/react":"👍"}}`, "#a")

	want := map[string]string{
		"c1 r1": "o o",
		"c1 r2": "r1 o",
		"c1 r3": "r2 o",
		"c1 r4": "x x",
		"c1 u1": "unknown unknown",
		"c1 s1": "s1 s1",
		"c2 b1": "r1 r1", // roots don't cross connections
	}
	got := repliesRows(t, d)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("replies:\n%v\nwant:\n%v", got, want)
	}
}
