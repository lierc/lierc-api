package db_test

import (
	"context"
	"reflect"
	"sort"
	"strconv"
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

	got, err := d.ArchiveCandidates(ctx, 2, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"#a:2", "status:1"}
	var have []string
	for _, c := range got {
		have = append(have, c.Channel+":"+strconv.Itoa(c.Movable))
	}
	if !reflect.DeepEqual(have, want) {
		t.Errorf("ArchiveCandidates = %v, want %v", have, want)
	}
	if got, err = d.ArchiveCandidates(ctx, 2, 2, []string{"nope"}); err != nil || len(got) != 0 {
		t.Errorf("ArchiveCandidates(nope) = %v, %v", got, err)
	}

	b, ok, err := d.ArchiveBlock(ctx, "c1", "#a", 2, 2)
	if err != nil || !ok || b.FirstID != 1 || b.LastID != 2 || b.Count != 2 {
		t.Errorf("ArchiveBlock = %+v, %v, %v", b, ok, err)
	}
	if _, ok, err = d.ArchiveBlock(ctx, "c1", "#a", 2, 5); err != nil || ok {
		t.Errorf("ArchiveBlock with too few = %v, %v, want false", ok, err)
	}

	b, ok, err = d.DropStatusBlock(ctx, "c1", "status", 1, 5)
	if err != nil || !ok || b.FirstID != 7 || b.LastID != 8 || b.Count != 2 {
		t.Errorf("DropStatusBlock = %+v, %v, %v", b, ok, err)
	}
	if _, ok, err = d.DropStatusBlock(ctx, "c1", "status", 1, 5); err != nil || ok {
		t.Errorf("DropStatusBlock again = %v, %v, want false", ok, err)
	}
}

func TestReviveCandidates(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a, b := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob")
	testenv.AddConnection(t, d, "c1", a, `{"x":1}`)
	testenv.AddConnection(t, d, "c2", a, "{}")
	testenv.AddConnection(t, d, "c3", b, "{}")
	exec(t, d, `UPDATE connections SET liercd = 'next' WHERE id = 'c2'`)
	exec(t, d, `UPDATE users SET last_login = now() WHERE id = $1`, a)
	exec(t, d, `UPDATE users SET last_login = now() - interval '10 days' WHERE id = $1`, b)

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

// ArchiveCandidates' boundaries (keep 2, block 3): the newest keep messages
// stay, however old the rest is (age doesn't count), and the count stops at
// a block.
func TestArchiveCandidatesBoundaries(t *testing.T) {
	d := testenv.Postgres(t)
	ctx := context.Background()
	a := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", a, "{}")
	old, fresh := "2020-01-01", "2099-01-01"
	for i := int64(21); i <= 23; i++ { // one past keep
		msgRow(t, d, i, "c1", a, "#keep", "x", false, false, old)
	}
	for i := int64(31); i <= 32; i++ { // exactly keep
		msgRow(t, d, i, "c1", a, "#exact", "x", false, false, old)
	}
	for i := int64(41); i <= 45; i++ { // two old, then three from the future: all but keep move
		when := old
		if i > 42 {
			when = fresh
		}
		msgRow(t, d, i, "c1", a, "#fresh", "x", false, false, when)
	}
	for i := int64(51); i <= 60; i++ { // eight movable, capped at a block
		msgRow(t, d, i, "c1", a, "#cap", "x", false, false, old)
	}
	got, err := d.ArchiveCandidates(ctx, 2, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	var have []string
	for _, c := range got {
		have = append(have, c.Channel+":"+strconv.Itoa(c.Movable))
	}
	want := []string{"#cap:3", "#fresh:3", "#keep:1"}
	if !reflect.DeepEqual(have, want) {
		t.Errorf("ArchiveCandidates = %v, want %v", have, want)
	}
}
