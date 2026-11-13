package db_test

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/testenv"
)

func TestArchiveBlocks(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	for i := int64(1); i <= 6; i++ {
		msgRow(t, d, i, "c1", a, "#a", "x", false, false, "2020-01-01")
	}
	for i := int64(7); i <= 9; i++ {
		msgRow(t, d, i, "c1", a, "status", "x", false, false, "2020-01-01")
	}
	cutoff := time.Now().Add(-time.Hour)

	got, err := d.ArchiveCandidates(ctx, 2, cutoff, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"#a:2", "status:1"}
	var have []string
	for _, c := range got {
		have = append(have, c.Channel+":"+string(rune('0'+c.Movable)))
	}
	if !reflect.DeepEqual(have, want) {
		t.Errorf("ArchiveCandidates = %v, want %v", have, want)
	}
	if got, err = d.ArchiveCandidates(ctx, 2, cutoff, 2, []string{"nope"}); err != nil || len(got) != 0 {
		t.Errorf("ArchiveCandidates(nope) = %v, %v", got, err)
	}

	b, ok, err := d.ArchiveBlock(ctx, "c1", "#a", 2, cutoff, 2)
	if err != nil || !ok || b.FirstID != 1 || b.LastID != 2 || b.Count != 2 {
		t.Errorf("ArchiveBlock = %+v, %v, %v", b, ok, err)
	}
	if _, ok, err = d.ArchiveBlock(ctx, "c1", "#a", 2, cutoff, 5); err != nil || ok {
		t.Errorf("ArchiveBlock with too few = %v, %v, want false", ok, err)
	}

	b, ok, err = d.DropStatusBlock(ctx, "c1", "status", 1, cutoff, 5)
	if err != nil || !ok || b.FirstID != 7 || b.LastID != 8 || b.Count != 2 {
		t.Errorf("DropStatusBlock = %+v, %v, %v", b, ok, err)
	}
	if _, ok, err = d.DropStatusBlock(ctx, "c1", "status", 1, cutoff, 5); err != nil || ok {
		t.Errorf("DropStatusBlock again = %v, %v, want false", ok, err)
	}
}

func TestOrphans(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	msgRow(t, d, 1, "c1", a, "#a", "x", false, false, "2020-01-01")
	for i := int64(2); i <= 4; i++ {
		msgRow(t, d, i, "gone", a, "#a", "x", false, false, "2020-01-01")
	}
	msgRow(t, d, 5, "c1", "ghost", "#a", "x", false, false, "2020-01-01")
	exec(t, d, `INSERT INTO log_archive (connection, "user", channel, first_id, last_id, min_time, max_time, count, messages)
		VALUES ('gone', $1, '#a', 10, 11, now(), now(), 2, '[]'), ('c1', $1, '#a', 12, 13, now(), now(), 2, '[]')`, a)

	cs, err := d.GoneConnections(ctx)
	if err != nil || !reflect.DeepEqual(cs, []string{"gone"}) {
		t.Errorf("GoneConnections = %v, %v", cs, err)
	}
	us, err := d.GoneUsers(ctx)
	if err != nil || !reflect.DeepEqual(us, []string{"ghost"}) {
		t.Errorf("GoneUsers = %v, %v", us, err)
	}
	if n, err := d.CountOrphanArchive(ctx); err != nil || n != 1 {
		t.Errorf("CountOrphanArchive = %d, %v", n, err)
	}

	if n, err := d.DeleteGoneConnectionLogs(ctx, "gone", 2); err != nil || n != 2 {
		t.Errorf("first batch = %d, %v", n, err)
	}
	if n, err := d.DeleteGoneConnectionLogs(ctx, "gone", 2); err != nil || n != 1 {
		t.Errorf("second batch = %d, %v", n, err)
	}
	if n, err := d.DeleteGoneConnectionLogs(ctx, "c1", 5); err != nil || n != 0 {
		t.Errorf("a live connection lost %d rows, %v", n, err)
	}
	if n, err := d.DeleteGoneUserLogs(ctx, a, 5); err != nil || n != 0 {
		t.Errorf("a live user lost %d rows, %v", n, err)
	}
	if n, err := d.DeleteGoneUserLogs(ctx, "ghost", 5); err != nil || n != 1 {
		t.Errorf("DeleteGoneUserLogs = %d, %v", n, err)
	}
	if n, err := d.DeleteOrphanArchive(ctx, 5); err != nil || n != 1 {
		t.Errorf("DeleteOrphanArchive = %d, %v", n, err)
	}
	var left int
	d.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM log) + (SELECT count(*) FROM log_archive)`).Scan(&left)
	if left != 2 {
		t.Errorf("%d rows left, want 2", left)
	}
}

func TestReviveCandidates(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a, b := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob")
	testenv.AddConnection(t, d, "c1", a, `{"x":1}`)
	testenv.AddConnection(t, d, "c2", a, "{}")
	testenv.AddConnection(t, d, "c3", b, "{}")
	exec(t, d, `UPDATE connection SET liercd = 'next' WHERE id = 'c2'`)
	exec(t, d, `UPDATE "user" SET last_login = now() WHERE id = $1`, a)
	exec(t, d, `UPDATE "user" SET last_login = now() - interval '10 days' WHERE id = $1`, b)

	ids := func(instance string) []string {
		cs, err := d.ReviveCandidates(ctx, 48*time.Hour, instance)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range cs {
			out = append(out, c.ID)
		}
		sort.Strings(out)
		return out
	}
	if got := ids(""); !reflect.DeepEqual(got, []string{"c1", "c2"}) {
		t.Errorf("all = %v", got)
	}
	if got := ids("main"); !reflect.DeepEqual(got, []string{"c1"}) {
		t.Errorf("main = %v", got)
	}
	if got := ids("next"); !reflect.DeepEqual(got, []string{"c2"}) {
		t.Errorf("next = %v", got)
	}
	cs, _ := d.ReviveCandidates(ctx, 48*time.Hour, "main")
	if len(cs) != 1 || !testenv.SameJSON(cs[0].Config, `{"x":1}`) {
		t.Errorf("config = %+v", cs)
	}
}
