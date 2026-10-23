package db_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lierc/lierc-api/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

var ctx = context.Background()

func TestPingAndUsers(t *testing.T) {
	d := testenv.Postgres(t)
	if err := d.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	id, token, err := d.AddUser(ctx, "alice", "a@example.com", "hashed")
	if err != nil || id == "" || token == "" || id == token {
		t.Fatalf("AddUser = %q %q %v", id, token, err)
	}
	if ok, err := d.UserExists(ctx, id); err != nil || !ok {
		t.Fatalf("UserExists = %v %v", ok, err)
	}
	if ok, _ := d.UserExists(ctx, "nope"); ok {
		t.Fatal("unexpected user")
	}
	u, err := d.LookupUser(ctx, id)
	if err != nil || u == nil || u.ID != id || u.Username != "alice" || u.Email != "a@example.com" {
		t.Fatalf("LookupUser = %+v %v", u, err)
	}
	if u, err := d.LookupUser(ctx, "nope"); err != nil || u != nil {
		t.Fatalf("LookupUser missing = %+v %v", u, err)
	}
	if _, _, err := d.AddUser(ctx, "alice", "b@example.com", "x"); err == nil {
		t.Fatal("duplicate username accepted")
	}
	if err := d.SaveLastLogin(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestConnections(t *testing.T) {
	d := testenv.Postgres(t)
	if err := d.SaveConnection(ctx, "c1", "u1", []byte(`{"Host":"irc.example.com"}`)); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveConnection(ctx, "c2", "u2", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	conns, err := d.Connections(ctx, "u1")
	if err != nil || len(conns) != 1 || conns[0].ID != "c1" {
		t.Fatalf("Connections = %+v %v", conns, err)
	}
	var cfg map[string]string
	if err := json.Unmarshal(conns[0].Config, &cfg); err != nil || cfg["Host"] != "irc.example.com" {
		t.Fatalf("config = %s %v", conns[0].Config, err)
	}
	if conns, _ := d.Connections(ctx, "nobody"); conns == nil || len(conns) != 0 {
		t.Fatalf("empty Connections = %#v", conns)
	}

	cfgB, ok, err := d.LookupConfig(ctx, "c1")
	if err != nil || !ok || string(cfgB) != `{"Host":"irc.example.com"}` {
		t.Fatalf("LookupConfig = %s %v %v", cfgB, ok, err)
	}
	if _, ok, _ := d.LookupConfig(ctx, "zz"); ok {
		t.Fatal("LookupConfig found missing")
	}
	owner, ok, err := d.LookupOwner(ctx, "c1")
	if err != nil || !ok || owner != "u1" {
		t.Fatalf("LookupOwner = %q %v %v", owner, ok, err)
	}
	if _, ok, _ := d.LookupOwner(ctx, "zz"); ok {
		t.Fatal("LookupOwner found missing")
	}

	if err := d.UpdateConfig(ctx, "c1", []byte(`{"Host":"new"}`)); err != nil {
		t.Fatal(err)
	}
	cfgB, _, _ = d.LookupConfig(ctx, "c1")
	if string(cfgB) != `{"Host":"new"}` {
		t.Fatalf("after update: %s", cfgB)
	}

	if err := d.DeleteConnection(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.LookupConfig(ctx, "c1"); ok {
		t.Fatal("connection not deleted")
	}
}

func TestVerifyOwner(t *testing.T) {
	d := testenv.Postgres(t)
	d.SaveConnection(ctx, "c1", "u1", []byte(`{}`))
	if ok, err := d.VerifyOwner(ctx, "c1", "u1"); err != nil || !ok {
		t.Fatalf("owner = %v %v", ok, err)
	}
}

func TestVerifyOwnerOtherUser(t *testing.T) {
	d := testenv.Postgres(t)
	d.SaveConnection(ctx, "c1", "u1", []byte(`{}`))
	if ok, err := d.VerifyOwner(ctx, "c1", "u2"); err != nil || ok {
		t.Fatalf("other user = %v %v", ok, err)
	}
}

func TestCheckTokenSingleUse(t *testing.T) {
	d := testenv.Postgres(t)
	tok, err := d.GetToken(ctx, "u1")
	if err != nil || tok == "" {
		t.Fatalf("GetToken = %q %v", tok, err)
	}
	if ok, err := d.CheckToken(ctx, "u1", tok); err != nil || !ok {
		t.Fatalf("first = %v %v", ok, err)
	}
	if ok, err := d.CheckToken(ctx, "u1", tok); err != nil || ok {
		t.Fatalf("second = %v %v", ok, err)
	}
}

func TestCheckTokenWrongUser(t *testing.T) {
	d := testenv.Postgres(t)
	tok, _ := d.GetToken(ctx, "u1")
	if ok, _ := d.CheckToken(ctx, "u2", tok); ok {
		t.Fatal("token accepted for other user")
	}
}
