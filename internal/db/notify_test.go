package db_test

import (
	"context"
	"testing"

	"github.com/lierc/lierc-api/internal/testenv"
)

func TestLookupConnection(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "ann")
	testenv.AddConnection(t, d, "c1", u, `{"Host":"irc.libera.chat","Alias":"Libera","Nick":"ann"}`)

	owner, cfg, ok, err := d.LookupConnection(context.Background(), "c1")
	if err != nil || !ok || owner != u || string(cfg) == "" {
		t.Fatalf("c1: owner %q cfg %s ok %v err %v", owner, cfg, ok, err)
	}
	if _, _, ok, err := d.LookupConnection(context.Background(), "nope"); err != nil || ok {
		t.Fatalf("unknown: ok %v err %v", ok, err)
	}
}
