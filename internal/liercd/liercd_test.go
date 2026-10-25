package liercd_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

var ctx = context.Background()

func client(f *testenv.LiercdServer) *liercd.Client {
	c := liercd.New("unused")
	c.BaseURL = f.URL
	return c
}

func TestDo(t *testing.T) {
	f := testenv.FakeLiercd(t)
	f.Respond("POST", "/x/y", 418, "teapot")
	r, err := client(f).Do(ctx, "POST", "x/y", []byte("hi"))
	if err != nil || r.Status != 418 || string(r.Body) != "teapot" {
		t.Fatalf("Do = %+v %v", r, err)
	}
	if c := f.Calls(); len(c) != 1 || c[0] != (testenv.LiercdCall{Method: "POST", Path: "/x/y", Body: "hi"}) {
		t.Fatalf("calls = %+v", c)
	}
}

func TestFindOrRecreateExisting(t *testing.T) {
	d := testenv.Postgres(t)
	f := testenv.FakeLiercd(t)
	testenv.AddConnection(t, d, "c1", "u1", `{"a":1}`)
	f.Respond("GET", "/c1/status", 200, `{"Status":"ok"}`)
	body, err := client(f).FindOrRecreate(ctx, d, "c1")
	if err != nil || string(body) != `{"Status":"ok"}` {
		t.Fatalf("got %s %v", body, err)
	}
	if n := len(f.Calls()); n != 1 {
		t.Fatalf("calls = %d", n)
	}
}

func TestFindOrRecreateCreates(t *testing.T) {
	d := testenv.Postgres(t)
	f := testenv.FakeLiercd(t)
	testenv.AddConnection(t, d, "c1", "u1", `{"a":1}`)
	f.SetStatus("c1", `{"Status":"new"}`)
	body, err := client(f).FindOrRecreate(ctx, d, "c1")
	if err != nil || string(body) != `{"Status":"new"}` {
		t.Fatalf("got %s %v", body, err)
	}
	want := []testenv.LiercdCall{
		{Method: "GET", Path: "/c1/status"},
		{Method: "POST", Path: "/c1/create", Body: `{"a":1}`},
		{Method: "GET", Path: "/c1/status"},
	}
	got := f.Calls()
	if len(got) != 3 {
		t.Fatalf("calls = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestFindOrRecreateMissingConnection(t *testing.T) {
	d := testenv.Postgres(t)
	f := testenv.FakeLiercd(t)
	_, err := client(f).FindOrRecreate(ctx, d, "nope")
	if err == nil || err.Error() != "Connection does not exist" {
		t.Fatalf("err = %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Fatal("liercd contacted")
	}
}

func TestFindOrRecreateCreateFails(t *testing.T) {
	d := testenv.Postgres(t)
	f := testenv.FakeLiercd(t)
	testenv.AddConnection(t, d, "c1", "u1", `{}`)
	f.Respond("POST", "/c1/create", 500, "boom")
	_, err := client(f).FindOrRecreate(ctx, d, "c1")
	if err == nil || err.Error() != "Unable to create new connection: 500 Internal Server Error" {
		t.Fatalf("err = %v", err)
	}
}

func TestFindOrRecreateStatusStillFails(t *testing.T) {
	d := testenv.Postgres(t)
	f := testenv.FakeLiercd(t)
	testenv.AddConnection(t, d, "c1", "u1", `{}`)
	f.Respond("GET", "/c1/status", 404, "")
	_, err := client(f).FindOrRecreate(ctx, d, "c1")
	if err == nil || !strings.HasPrefix(err.Error(), "Unable to create new connection: 404 Not Found") {
		t.Fatalf("err = %v", err)
	}
}
