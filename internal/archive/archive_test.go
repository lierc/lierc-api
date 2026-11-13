package archive

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

var (
	now   = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	old   = now.Add(-60 * 24 * time.Hour)
	fresh = now.Add(-24 * time.Hour)
)

func opts() Options {
	return Options{Keep: 3, Block: 2, MinAge: 720 * time.Hour, Now: now}
}

type tdb struct {
	t     *testing.T
	pool  *pgxpool.Pool
	store db.Store
}

func newDB(t *testing.T) *tdb {
	d := testenv.Postgres(t)
	return &tdb{t: t, pool: d.Pool.Pool, store: d}
}

func (d *tdb) exec(sql string, args ...any) {
	d.t.Helper()
	if _, err := d.pool.Exec(context.Background(), sql, args...); err != nil {
		d.t.Fatalf("%s: %v", sql, err)
	}
}

func (d *tdb) count(sql string, args ...any) int {
	d.t.Helper()
	var n int
	if err := d.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		d.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// insert adds a PRIVMSG with an explicit id and time.
func (d *tdb) insert(id int, conn, channel string, at time.Time) {
	d.t.Helper()
	msg := fmt.Sprintf(`{"Command":"PRIVMSG","Params":[%q,"message %d"],"Prefix":{"Name":"nick"}}`, channel, id)
	d.exec(`INSERT INTO log (id, connection, "user", channel, command, highlight, time, message)
		VALUES ($1, $2, $3, $4, 'PRIVMSG', $5, $6::timestamp, $7)`,
		id, conn, "u-"+conn, channel, id%3 == 0, at.Format("2006-01-02 15:04:05.999999"), msg)
}

func (d *tdb) logIDs(conn, channel string) []int {
	d.t.Helper()
	rows, err := d.pool.Query(context.Background(),
		`SELECT id FROM log WHERE connection = $1 AND channel = $2 ORDER BY id`, conn, channel)
	if err != nil {
		d.t.Fatal(err)
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			d.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		d.t.Fatal(err)
	}
	return ids
}

type blk struct{ first, last, count int }

func (d *tdb) archiveBlocks(conn, channel string) []blk {
	d.t.Helper()
	rows, err := d.pool.Query(context.Background(),
		`SELECT first_id, last_id, count FROM log_archive WHERE connection = $1 AND channel = $2 ORDER BY first_id`, conn, channel)
	if err != nil {
		d.t.Fatal(err)
	}
	defer rows.Close()
	var bs []blk
	for rows.Next() {
		var b blk
		if err := rows.Scan(&b.first, &b.last, &b.count); err != nil {
			d.t.Fatal(err)
		}
		bs = append(bs, b)
	}
	if err := rows.Err(); err != nil {
		d.t.Fatal(err)
	}
	return bs
}

func ids(from, to int) []int {
	var r []int
	for i := from; i <= to; i++ {
		r = append(r, i)
	}
	return r
}

// moveAll calls MoveBlock until it returns false and returns the blocks moved.
func moveAll(t *testing.T, d *tdb, c Channel, o Options) []Block {
	t.Helper()
	var bs []Block
	for i := 0; i < 100; i++ {
		b, ok, err := MoveBlock(context.Background(), d.store, c, o)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return bs
		}
		bs = append(bs, b)
	}
	t.Fatal("MoveBlock never returned false")
	return nil
}

func TestMoveKeepsNewest(t *testing.T) {
	d := newDB(t)
	for i := 1; i <= 10; i++ {
		d.insert(i, "c1", "#chan", old.Add(time.Duration(i)*time.Minute))
	}
	c := Channel{Connection: "c1", Channel: "#chan"}
	bs := moveAll(t, d, c, opts())
	want := []Block{
		{"c1", "#chan", 1, 2, 2},
		{"c1", "#chan", 3, 4, 2},
		{"c1", "#chan", 5, 6, 2},
	}
	if !reflect.DeepEqual(bs, want) {
		t.Fatalf("blocks %+v, want %+v", bs, want)
	}
	if got := d.archiveBlocks("c1", "#chan"); !reflect.DeepEqual(got, []blk{{1, 2, 2}, {3, 4, 2}, {5, 6, 2}}) {
		t.Fatalf("archive %v", got)
	}
	if got := d.logIDs("c1", "#chan"); !reflect.DeepEqual(got, ids(7, 10)) {
		t.Fatalf("log ids %v", got)
	}
}

func TestMoveRespectsMinAge(t *testing.T) {
	d := newDB(t)
	for i := 1; i <= 10; i++ {
		at := old.Add(time.Duration(i) * time.Minute)
		if i >= 7 {
			at = fresh.Add(time.Duration(i) * time.Minute)
		}
		d.insert(i, "c1", "#chan", at)
	}
	moveAll(t, d, Channel{Connection: "c1", Channel: "#chan"}, opts())
	if got := d.archiveBlocks("c1", "#chan"); !reflect.DeepEqual(got, []blk{{1, 2, 2}, {3, 4, 2}, {5, 6, 2}}) {
		t.Fatalf("archive %v", got)
	}
	if got := d.logIDs("c1", "#chan"); !reflect.DeepEqual(got, ids(7, 10)) {
		t.Fatalf("log ids %v", got)
	}

	// Two old messages fewer: archivable is 1-4 by age, and 5-6 stay.
	d2 := newDB(t)
	for i := 1; i <= 10; i++ {
		at := old.Add(time.Duration(i) * time.Minute)
		if i >= 5 {
			at = fresh.Add(time.Duration(i) * time.Minute)
		}
		d2.insert(i, "c1", "#chan", at)
	}
	moveAll(t, d2, Channel{Connection: "c1", Channel: "#chan"}, opts())
	if got := d2.archiveBlocks("c1", "#chan"); !reflect.DeepEqual(got, []blk{{1, 2, 2}, {3, 4, 2}}) {
		t.Fatalf("archive %v", got)
	}
}

func TestMoveSplitsByID(t *testing.T) {
	d := newDB(t)
	for i := 1; i <= 10; i++ {
		at := old.Add(time.Duration(i) * time.Minute)
		if i == 2 {
			at = fresh
		}
		d.insert(i, "c1", "#chan", at)
	}
	moveAll(t, d, Channel{Connection: "c1", Channel: "#chan"}, opts())
	if got := d.archiveBlocks("c1", "#chan"); !reflect.DeepEqual(got, []blk{{1, 2, 2}, {3, 4, 2}, {5, 6, 2}}) {
		t.Fatalf("archive %v", got)
	}
	if got := d.logIDs("c1", "#chan"); !reflect.DeepEqual(got, ids(7, 10)) {
		t.Fatalf("log ids %v", got)
	}
}

func TestMovePerConnection(t *testing.T) {
	d := newDB(t)
	// c1 has ids 1-5 interleaved with c2's 6-15 (odd ids for c1 among the first ten).
	id := 1
	var c1, c2 []int
	for i := 0; i < 15; i++ {
		conn := "c2"
		if i%3 == 0 {
			conn = "c1"
		}
		d.insert(id, conn, "#chan", old.Add(time.Duration(id)*time.Minute))
		if conn == "c1" {
			c1 = append(c1, id)
		} else {
			c2 = append(c2, id)
		}
		id++
	}
	// c1: 5 messages, keep 3 → 2 movable; c2: 10 messages → 7 movable, but
	// Candidates counts only up to a block (2).
	cs, err := Candidates(context.Background(), d.store, opts())
	if err != nil {
		t.Fatal(err)
	}
	want := []Channel{
		{Connection: "c1", Channel: "#chan", Movable: 2},
		{Connection: "c2", Channel: "#chan", Movable: 2},
	}
	if !reflect.DeepEqual(cs, want) {
		t.Fatalf("candidates %+v, want %+v", cs, want)
	}

	bs := moveAll(t, d, want[0], opts())
	if len(bs) != 1 || bs[0].FirstID != c1[0] || bs[0].LastID != c1[1] || bs[0].Count != 2 {
		t.Fatalf("c1 blocks %+v", bs)
	}
	if got := d.logIDs("c2", "#chan"); !reflect.DeepEqual(got, c2) {
		t.Fatalf("c2 log ids %v, want %v", got, c2)
	}
	if got := d.logIDs("c1", "#chan"); !reflect.DeepEqual(got, c1[2:]) {
		t.Fatalf("c1 log ids %v, want %v", got, c1[2:])
	}
	if n := d.count(`SELECT count(*) FROM log_archive WHERE connection = 'c2'`); n != 0 {
		t.Fatalf("c2 has %d archive rows", n)
	}

	bs = moveAll(t, d, want[1], opts())
	if len(bs) != 3 {
		t.Fatalf("c2 blocks %+v", bs)
	}
	if got := d.logIDs("c2", "#chan"); !reflect.DeepEqual(got, c2[6:]) {
		t.Fatalf("c2 log ids %v, want %v", got, c2[6:])
	}
	if got := d.logIDs("c1", "#chan"); !reflect.DeepEqual(got, c1[2:]) {
		t.Fatalf("c1 log ids %v after c2 moved", got)
	}
}

func TestMoveElements(t *testing.T) {
	d := newDB(t)
	times := []string{
		"2022-05-13 20:26:19.241197",
		"2026-09-01 00:00:00.5",
		"2026-09-01 00:00:00",
		"2026-09-01 23:59:59.999999",
	}
	for i, ts := range times {
		d.exec(`INSERT INTO log (id, connection, "user", channel, command, highlight, time, message)
			VALUES ($1, 'c1', 'u1', '#chan', 'PRIVMSG', $2, $3::timestamp, $4)`,
			i+1, i%2 == 0, ts,
			fmt.Sprintf(`{"Command":"PRIVMSG","Params":["#chan","héllo \"%d\" ☃"],"Prefix":{"Name":"nick","Self":true},"Tags":{"msgid":"m%d"}}`, i, i))
	}
	for i := 5; i <= 7; i++ {
		d.insert(i, "c1", "#chan", fresh)
	}
	// Snapshot the rows before they move.
	d.exec(`CREATE TABLE before AS SELECT * FROM log WHERE id <= 4`)

	o := opts()
	o.Block = 4
	b, ok, err := MoveBlock(context.Background(), d.store, Channel{Connection: "c1", Channel: "#chan"}, o)
	if err != nil || !ok {
		t.Fatalf("MoveBlock: %v %v", ok, err)
	}
	if b != (Block{"c1", "#chan", 1, 4, 4}) {
		t.Fatalf("block %+v", b)
	}

	if n := d.count(`SELECT count(*) FROM log_archive`); n != 1 {
		t.Fatalf("%d archive rows", n)
	}
	if n := d.count(`SELECT json_array_length(messages) FROM log_archive`); n != 4 {
		t.Fatalf("%d elements", n)
	}
	// Keys exactly id,time,message,highlight on every element.
	if n := d.count(`SELECT count(*) FROM log_archive a, json_array_elements(a.messages) e
		WHERE (SELECT array_agg(k ORDER BY k) FROM json_object_keys(e) k) <> ARRAY['highlight','id','message','time']`); n != 0 {
		t.Fatalf("%d elements with other keys", n)
	}
	if n := d.count(`SELECT count(*) FROM log_archive a, json_array_elements(a.messages) e
		WHERE json_typeof(e->'time') <> 'number' OR json_typeof(e->'id') <> 'number' OR json_typeof(e->'highlight') <> 'boolean'`); n != 0 {
		t.Fatalf("%d elements with wrong types", n)
	}
	// Elements in id order.
	if n := d.count(`SELECT count(*) FROM log_archive a, json_array_elements(a.messages) WITH ORDINALITY e(v, n)
		WHERE (v->>'id')::int <> n`); n != 0 {
		t.Fatalf("%d elements out of order", n)
	}
	// Each element matches its original row.
	if n := d.count(`SELECT count(*) FROM log_archive a, json_array_elements(a.messages) e
		JOIN before b ON b.id = (e->>'id')::int
		WHERE to_timestamp((e->>'time')::bigint) AT TIME ZONE 'UTC' = date_trunc('second', b.time)
		  AND (e->'message')::jsonb = b.message
		  AND (e->>'highlight')::bool = b.highlight`); n != 4 {
		t.Fatalf("%d of 4 elements match their rows", n)
	}
	// The same must hold whatever the session time zone.
	if n := d.count(`SELECT count(*) FROM log_archive a, json_array_elements(a.messages) e
		JOIN before b ON b.id = (e->>'id')::int
		WHERE (e->>'time')::bigint = floor(extract(epoch from b.time))::bigint`); n != 4 {
		t.Fatalf("%d of 4 element times equal the epoch seconds", n)
	}
	if n := d.count(`SELECT count(*) FROM log_archive a
		WHERE a."user" = 'u1'
		  AND a.min_time = (SELECT min(time) FROM before)
		  AND a.max_time = (SELECT max(time) FROM before)
		  AND a.first_id = 1 AND a.last_id = 4 AND a.count = 4`); n != 1 {
		t.Fatal("archive row's user, min_time or max_time do not match")
	}
	if got := d.logIDs("c1", "#chan"); !reflect.DeepEqual(got, ids(5, 7)) {
		t.Fatalf("log ids %v", got)
	}
}

func TestCandidatesConnections(t *testing.T) {
	d := newDB(t)
	id := 1
	for _, conn := range []string{"c1", "c2"} {
		for _, ch := range []string{"#a", "#b"} {
			for i := 0; i < 5; i++ {
				d.insert(id, conn, ch, old.Add(time.Duration(id)*time.Minute))
				id++
			}
		}
	}
	o := opts()
	o.Connections = []string{"c1"}
	cs, err := Candidates(context.Background(), d.store, o)
	if err != nil {
		t.Fatal(err)
	}
	want := []Channel{
		{Connection: "c1", Channel: "#a", Movable: 2},
		{Connection: "c1", Channel: "#b", Movable: 2},
	}
	if !reflect.DeepEqual(cs, want) {
		t.Fatalf("candidates %+v, want %+v", cs, want)
	}

	o.Connections = []string{"nope"}
	cs, err = Candidates(context.Background(), d.store, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 0 {
		t.Fatalf("unknown connection lists %+v", cs)
	}

	o.Connections = nil
	cs, err = Candidates(context.Background(), d.store, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 4 {
		t.Fatalf("all connections list %+v", cs)
	}
}

func TestDropStatus(t *testing.T) {
	d := newDB(t)
	for i := 1; i <= 10; i++ {
		d.insert(i, "c1", "status", old.Add(time.Duration(i)*time.Minute))
	}
	o := opts()
	cs, err := Candidates(context.Background(), d.store, o)
	if err != nil {
		t.Fatal(err)
	}
	// 7 below the cutoff, counted up to a block (2)
	st := Channel{Connection: "c1", Channel: "status", Movable: 2, Status: true}
	if !reflect.DeepEqual(cs, []Channel{st}) {
		t.Fatalf("candidates %+v", cs)
	}

	if _, _, err := MoveBlock(context.Background(), d.store, st, o); err == nil {
		t.Fatal("MoveBlock on a status channel succeeded")
	}
	if _, _, err := DropStatus(context.Background(), d.store, Channel{Connection: "c1", Channel: "#chan"}, o); err == nil {
		t.Fatal("DropStatus on #chan succeeded")
	}

	var got []Block
	for i := 0; i < 10; i++ {
		b, ok, err := DropStatus(context.Background(), d.store, st, o)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got = append(got, b)
	}
	want := []Block{
		{"c1", "status", 1, 2, 2},
		{"c1", "status", 3, 4, 2},
		{"c1", "status", 5, 6, 2},
		{"c1", "status", 7, 7, 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dropped %+v, want %+v", got, want)
	}
	if ids := d.logIDs("c1", "status"); !reflect.DeepEqual(ids, []int{8, 9, 10}) {
		t.Fatalf("log ids %v", ids)
	}
	if n := d.count(`SELECT count(*) FROM log_archive`); n != 0 {
		t.Fatalf("%d archive rows", n)
	}
}

func TestCandidates(t *testing.T) {
	d := newDB(t)
	o := opts()
	// #short has Keep+Block-1 old messages, #full has Keep+Block.
	id := 1
	for i := 0; i < o.Keep+o.Block-1; i++ {
		d.insert(id, "c1", "#short", old.Add(time.Duration(id)*time.Minute))
		id++
	}
	for i := 0; i < o.Keep+o.Block; i++ {
		d.insert(id, "c1", "#full", old.Add(time.Duration(id)*time.Minute))
		id++
	}
	// #new has many messages, all recent.
	for i := 0; i < 10; i++ {
		d.insert(id, "c1", "#new", fresh.Add(time.Duration(id)*time.Minute))
		id++
	}
	// A status window with no more than Keep messages.
	for i := 0; i < o.Keep; i++ {
		d.insert(id, "c1", "status", old.Add(time.Duration(id)*time.Minute))
		id++
	}
	cs, err := Candidates(context.Background(), d.store, o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cs, []Channel{{Connection: "c1", Channel: "#full", Movable: o.Block}}) {
		t.Fatalf("candidates %+v", cs)
	}
}

func TestMoveConcurrentInsert(t *testing.T) {
	d := newDB(t)
	for i := 1; i <= 5; i++ {
		d.insert(i, "c1", "#chan", old.Add(time.Duration(i)*time.Minute))
	}
	o := opts()
	cs, err := Candidates(context.Background(), d.store, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Movable != 2 {
		t.Fatalf("candidates %+v", cs)
	}
	// The logger writes a new message after the candidates were chosen.
	d.insert(6, "c1", "#chan", now)

	b, ok, err := MoveBlock(context.Background(), d.store, cs[0], o)
	if err != nil || !ok {
		t.Fatalf("MoveBlock: %v %v", ok, err)
	}
	if b != (Block{"c1", "#chan", 1, 2, 2}) {
		t.Fatalf("block %+v", b)
	}
	if got := d.logIDs("c1", "#chan"); !reflect.DeepEqual(got, ids(3, 6)) {
		t.Fatalf("log ids %v", got)
	}
	// Every id is in exactly one table.
	var all []int
	all = append(all, d.logIDs("c1", "#chan")...)
	rows, err := d.pool.Query(context.Background(),
		`SELECT (e->>'id')::int FROM log_archive a, json_array_elements(a.messages) e`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		all = append(all, id)
	}
	rows.Close()
	sort.Ints(all)
	if !reflect.DeepEqual(all, ids(1, 6)) {
		t.Fatalf("ids across both tables %v", all)
	}
}

// Names starting with * (ZNC's *status and other modules, server notices
// like *.freenode.net) are never people: no nick can start with *. They're
// handled like the status window: deleted below the cutoff, never archived.
func TestStarNamesAreStatus(t *testing.T) {
	d := newDB(t)
	id := 1
	for _, ch := range []string{"*status", "*.freenode.net"} {
		for i := 0; i < 10; i++ {
			d.insert(id, "c1", ch, old.Add(time.Duration(id)*time.Minute))
			id++
		}
	}
	o := opts()
	cs, err := Candidates(context.Background(), d.store, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || !cs[0].Status || !cs[1].Status {
		t.Fatalf("candidates %+v, want both as status windows", cs)
	}
	if _, _, err := MoveBlock(context.Background(), d.store, cs[0], o); err == nil {
		t.Fatalf("MoveBlock on %s succeeded", cs[0].Channel)
	}
	for _, c := range cs {
		for {
			_, ok, err := DropStatus(context.Background(), d.store, c, o)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
		}
	}
	if n := d.count(`SELECT count(*) FROM log_archive`); n != 0 {
		t.Fatalf("%d archive rows", n)
	}
	if n := d.count(`SELECT count(*) FROM log`); n != 6 {
		t.Fatalf("%d log rows left, want 3 per name", n)
	}
}
