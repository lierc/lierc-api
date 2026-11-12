package liercd_test

import (
	"context"
	"errors"
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
		if got[i].Method != want[i].Method || got[i].Path != want[i].Path || got[i].Body != want[i].Body && !testenv.SameJSON(got[i].Body, want[i].Body) {
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

func TestRoutedDo(t *testing.T) {
	cases := []struct {
		name, instance, path, body string
		noNext                     bool
		want                       string // "main", "next", or "" for an error
		errHas                     string
	}{
		{name: "main", instance: "", path: "c1/status", want: "main"},
		{name: "next", instance: "next", path: "c1/status", want: "next"},
		{name: "leading slash", instance: "next", path: "/c1/status", want: "next"},
		{name: "body", instance: "next", path: "c1/raw", body: "PRIVMSG #a :hi", want: "next"},
		{name: "next unset", instance: "next", path: "c1/status", noNext: true,
			errHas: `liercd instance "next" has no host (LIERCD_NEXT_HOST)`},
		{name: "unknown", instance: "bogus", path: "c1/status", errHas: `unknown liercd instance "bogus"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, n := testenv.FakeLiercd(t), testenv.FakeLiercd(t)
			nextBase := n.URL
			if tc.noNext {
				nextBase = ""
			}
			var looked []string
			c := liercd.NewRoutedBase(m.URL, nextBase, func(_ context.Context, id string) (string, error) {
				looked = append(looked, id)
				return tc.instance, nil
			})
			method := "GET"
			var body []byte
			if tc.body != "" {
				method, body = "POST", []byte(tc.body)
			}
			_, err := c.Do(ctx, method, tc.path, body)
			if len(looked) != 1 || looked[0] != "c1" {
				t.Errorf("looked up %v, want [c1]", looked)
			}
			if tc.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want %q", err, tc.errHas)
				}
				if len(m.Calls())+len(n.Calls()) != 0 {
					t.Fatalf("liercd called: main %+v next %+v", m.Calls(), n.Calls())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			hit, miss := m, n
			if tc.want == "next" {
				hit, miss = n, m
			}
			wantCall := testenv.LiercdCall{Method: method, Path: "/" + strings.TrimLeft(tc.path, "/"), Body: tc.body}
			if c := hit.Calls(); len(c) != 1 || c[0] != wantCall {
				t.Errorf("%s calls = %+v, want %+v", tc.want, c, wantCall)
			}
			if c := miss.Calls(); len(c) != 0 {
				t.Errorf("other instance called: %+v", c)
			}
		})
	}
}

func TestRoutedDoLookupError(t *testing.T) {
	m := testenv.FakeLiercd(t)
	c := liercd.NewRoutedBase(m.URL, "", func(context.Context, string) (string, error) {
		return "", errors.New("db down")
	})
	if _, err := c.Do(ctx, "GET", "c1/status", nil); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("err = %v", err)
	}
	if len(m.Calls()) != 0 {
		t.Fatal("liercd called")
	}
}

func TestNewRoutedHosts(t *testing.T) {
	c := liercd.NewRouted(liercd.Instances{Main: "a", Next: "b"}, nil)
	if c.BaseURL != "http://a:5005" {
		t.Errorf("BaseURL = %q", c.BaseURL)
	}
}

// A host may give its own port; without one it's liercd's 5005.
func TestHostPort(t *testing.T) {
	for host, want := range map[string]string{
		"a":              "http://a:5005",
		"127.0.0.2":      "http://127.0.0.2:5005",
		"127.0.0.1:5006": "http://127.0.0.1:5006",
		"liercd-next:80": "http://liercd-next:80",
	} {
		if got := liercd.New(host).BaseURL; got != want {
			t.Errorf("New(%q).BaseURL = %q, want %q", host, got, want)
		}
	}
	c := liercd.NewRouted(liercd.Instances{Main: "127.0.0.1", Next: "127.0.0.1:5006"}, nil)
	if c.BaseURL != "http://127.0.0.1:5005" {
		t.Errorf("main BaseURL = %q", c.BaseURL)
	}
	next, err := c.For("next")
	if err != nil {
		t.Fatal(err)
	}
	if next.BaseURL != "http://127.0.0.1:5006" {
		t.Errorf("next BaseURL = %q", next.BaseURL)
	}
}

func TestFindOrRecreateRoutes(t *testing.T) {
	d := testenv.Postgres(t)
	m, n := testenv.FakeLiercd(t), testenv.FakeLiercd(t)
	testenv.AddConnection(t, d, "c1", "u1", `{"a":1}`)
	if _, err := d.Pool.Exec(ctx, `UPDATE connection SET liercd='next' WHERE id='c1'`); err != nil {
		t.Fatal(err)
	}
	n.SetStatus("c1", `{"Status":"new"}`)
	body, err := liercd.NewRoutedBase(m.URL, n.URL, d.LookupLiercd).FindOrRecreate(ctx, d, "c1")
	if err != nil || string(body) != `{"Status":"new"}` {
		t.Fatalf("got %s %v", body, err)
	}
	if got := n.Calls(); len(got) != 3 || got[1].Method != "POST" || got[1].Path != "/c1/create" || !testenv.SameJSON(got[1].Body, `{"a":1}`) {
		t.Fatalf("next calls = %+v", got)
	}
	if got := m.Calls(); len(got) != 0 {
		t.Fatalf("main calls = %+v", got)
	}
}

func TestFor(t *testing.T) {
	m, n := testenv.FakeLiercd(t), testenv.FakeLiercd(t)
	looked := 0
	c := liercd.NewRoutedBase(m.URL, n.URL, func(context.Context, string) (string, error) {
		looked++
		return "next", nil
	})
	for _, tc := range []struct {
		instance string
		hit      *testenv.LiercdServer
	}{{"main", m}, {"next", n}} {
		pinned, err := c.For(tc.instance)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pinned.Do(ctx, "GET", "c1/status", nil); err != nil {
			t.Fatal(err)
		}
		if got := tc.hit.Calls(); len(got) != 1 || got[0].Path != "/c1/status" {
			t.Errorf("%s calls = %+v", tc.instance, got)
		}
	}
	if looked != 0 {
		t.Errorf("a pinned client looked up the column %d times", looked)
	}
	if _, err := liercd.NewRoutedBase(m.URL, "", nil).For("next"); err == nil ||
		!strings.Contains(err.Error(), `liercd instance "next" has no host (LIERCD_NEXT_HOST)`) {
		t.Errorf("next without host: err = %v", err)
	}
	if _, err := c.For("bogus"); err == nil || !strings.Contains(err.Error(), `unknown liercd instance "bogus"`) {
		t.Errorf("bogus: err = %v", err)
	}
}
