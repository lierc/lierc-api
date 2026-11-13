package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

type harness struct {
	t     *testing.T
	d     *db.DB
	fake  *testenv.LiercdServer
	s     *Service
	mu    sync.Mutex
	said  []string // announcements, "<action> <id>"
	notes int      // NOTIFY highlights
}

func setup(t *testing.T) (*harness, string) {
	t.Helper()
	h := &harness{t: t, d: testenv.Postgres(t), fake: testenv.FakeLiercd(t)}
	h.s = &Service{
		DB:     h.d,
		Liercd: &liercd.Client{BaseURL: h.fake.URL, HTTP: &http.Client{}},
		Announce: func(_ context.Context, c db.ConnectionChange) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.said = append(h.said, c.Action+" "+c.Connection)
		},
		NotifyHighlights: func(context.Context) error { h.mu.Lock(); h.notes++; h.mu.Unlock(); return nil },
		CheckHost: func(_ context.Context, host string, _ int) error {
			if host == "bad.example" {
				return errors.New("bad.example is a private address")
			}
			return nil
		},
		Limit: 5,
	}
	return h, testenv.AddUser(t, h.d, "alice")
}

func (h *harness) row(id string) (map[string]any, bool) {
	h.t.Helper()
	raw, ok, err := h.d.LookupConfig(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	var m map[string]any
	if ok {
		json.Unmarshal(raw, &m)
	}
	return m, ok
}

func (h *harness) calls() []string {
	var out []string
	for _, c := range h.fake.Calls() {
		out = append(out, c.Method+" "+c.Path)
	}
	return out
}

func userMsg(t *testing.T, err error, want string) {
	t.Helper()
	msg, ok := IsUserError(err)
	if !ok || msg != want {
		t.Errorf("error = %v (user %v), want %q", err, ok, want)
	}
}

const valid = `{"Host":"irc.example","Port":6667,"Nick":"me","Channels":["#a"]}`

func TestCreate(t *testing.T) {
	h, a := setup(t)
	id, err := h.s.Create(context.Background(), a, []byte(valid))
	if err != nil || len(id) == 0 {
		t.Fatalf("Create = %q, %v", id, err)
	}
	cfg, ok := h.row(id)
	if !ok || cfg["Host"] != "irc.example" {
		t.Errorf("row = %v %v", cfg, ok)
	}
	var created bool
	h.d.Pool.QueryRow(context.Background(), `SELECT created IS NOT NULL FROM connection WHERE id=$1`, id).Scan(&created)
	if !created || fmt.Sprint(h.said) != "[create "+id+"]" || fmt.Sprint(h.calls()) != "[POST /"+id+"/create]" || h.notes != 1 {
		t.Errorf("created %v said %v calls %v notes %d", created, h.said, h.calls(), h.notes)
	}
	var sent map[string]any
	json.Unmarshal([]byte(h.fake.Calls()[0].Body), &sent)
	if sent["Host"] != "irc.example" || sent["Nick"] != "me" {
		t.Errorf("liercd got %s", h.fake.Calls()[0].Body)
	}
}

func TestCreateRefused(t *testing.T) {
	h, a := setup(t)
	old := newID
	newID = func() string { return "fixedid" }
	t.Cleanup(func() { newID = old })
	h.fake.Respond("POST", "/fixedid/create", 400, "nok\n")
	_, err := h.s.Create(context.Background(), a, []byte(valid))
	userMsg(t, err, "nok")
	if _, ok := h.row("fixedid"); ok || fmt.Sprint(h.said) != "[create fixedid delete fixedid]" {
		t.Errorf("row left %v, said %v", ok, h.said)
	}
}

func TestCreateUnreachable(t *testing.T) {
	h, a := setup(t)
	old := newID
	newID = func() string { return "fixedid" }
	t.Cleanup(func() { newID = old })
	h.s.Liercd = &liercd.Client{BaseURL: "http://127.0.0.1:1", HTTP: &http.Client{}}
	_, err := h.s.Create(context.Background(), a, []byte(valid))
	if msg, ok := IsUserError(err); !ok || !strings.Contains(msg, "refused") {
		t.Errorf("error = %v", err)
	}
	if _, ok := h.row("fixedid"); ok {
		t.Error("row left")
	}
}

func TestCreateInvalid(t *testing.T) {
	h, a := setup(t)
	for body, want := range map[string]string{
		`[1]`:           "Invalid connection settings",
		`x`:             "Invalid connection settings",
		`{"Port":6667}`: "Host is required",
		`{"Host":"bad.example","Port":6667,"Nick":"me"}`: "bad.example is a private address",
	} {
		_, err := h.s.Create(context.Background(), a, []byte(body))
		userMsg(t, err, want)
	}
	if len(h.fake.Calls()) != 0 {
		t.Errorf("liercd called: %v", h.calls())
	}
}

func TestCreateLimit(t *testing.T) {
	h, a := setup(t)
	for i := 0; i < 4; i++ {
		testenv.AddConnection(t, h.d, fmt.Sprint("c", i), a, "{}")
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = h.s.Create(context.Background(), a, []byte(valid))
		}(i)
	}
	wg.Wait()
	ok, limited := 0, 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if msg, _ := IsUserError(err); msg == "You can have at most 5 connections" {
			limited++
		} else {
			t.Errorf("unexpected %v", err)
		}
	}
	if ok != 1 || limited != 1 {
		t.Errorf("%d ok, %d limited", ok, limited)
	}
	_, err := h.s.Create(context.Background(), a, []byte(valid))
	userMsg(t, err, "You can have at most 5 connections")
}

func TestEditAliasOnly(t *testing.T) {
	h, a := setup(t)
	testenv.AddConnection(t, h.d, "c1", a, `{"Host":"irc.example","Port":6667,"Nick":"me","Channels":["#a"]}`)
	err := h.s.Edit(context.Background(), a, "c1", []byte(`{"Host":"irc.example","Port":6667,"Nick":"me","Alias":"Net"}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := h.row("c1")
	if cfg["Alias"] != "Net" || fmt.Sprint(cfg["Channels"]) != "[#a]" || len(h.fake.Calls()) != 0 || fmt.Sprint(h.said) != "[update c1]" || h.notes != 1 {
		t.Errorf("cfg %v calls %v said %v notes %d", cfg, h.calls(), h.said, h.notes)
	}
}

func TestEditReconnects(t *testing.T) {
	h, a := setup(t)
	testenv.AddConnection(t, h.d, "c1", a, `{"Host":"irc.example","Port":6667,"Nick":"me","Channels":["#a"]}`)
	h.fake.Respond("POST", "/c1/destroy", 200, "ok")
	if err := h.s.Edit(context.Background(), a, "c1", []byte(`{"Host":"irc.other","Port":6667,"Nick":"me"}`)); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(h.calls()) != "[POST /c1/destroy POST /c1/create]" || fmt.Sprint(h.said) != "[update c1]" {
		t.Errorf("calls %v said %v", h.calls(), h.said)
	}
	cfg, _ := h.row("c1")
	if cfg["Host"] != "irc.other" || fmt.Sprint(cfg["Channels"]) != "[#a]" {
		t.Errorf("cfg %v", cfg)
	}
	h.fake.Respond("POST", "/c1/destroy", 404, "nok\n")
	if err := h.s.Edit(context.Background(), a, "c1", []byte(`{"Host":"irc.third","Port":6667,"Nick":"me"}`)); err != nil {
		t.Errorf("destroy 404: %v", err)
	}
	h.fake.Respond("POST", "/c1/create", 500, "boom")
	err := h.s.Edit(context.Background(), a, "c1", []byte(`{"Host":"irc.fourth","Port":6667,"Nick":"me"}`))
	userMsg(t, err, "boom")
}

// mergeDuring makes liercd's answer to POST path wait for a merge of
// fields into c1, as if liercd joined a channel just then, for up to
// 300ms: with the row locked the merge waits for the edit instead. It
// returns the merge's result, once it's done.
func (h *harness) mergeDuring(path, fields string) <-chan error {
	done := make(chan error, 1)
	h.fake.OnCall("POST", path, func() {
		merged := make(chan error, 1)
		go func() { merged <- h.d.MergeConfig(context.Background(), "c1", []byte(fields)) }()
		select {
		case err := <-merged:
			done <- err
		case <-time.After(300 * time.Millisecond):
			go func() { done <- <-merged }()
		}
	})
	return done
}

// A channel liercd joins while a reconnecting edit destroys the
// connection is saved on top of the edit, not overwritten by it.
func TestEditKeepsMergeDuringDestroy(t *testing.T) {
	h, a := setup(t)
	testenv.AddConnection(t, h.d, "c1", a, `{"Host":"irc.example","Port":6667,"Nick":"me","Channels":["#a"]}`)
	h.fake.Respond("POST", "/c1/destroy", 200, "ok")
	merged := h.mergeDuring("/c1/destroy", `{"Channels":["#a","#new"]}`)
	if err := h.s.Edit(context.Background(), a, "c1", []byte(`{"Host":"irc.other","Port":6667,"Nick":"me"}`)); err != nil {
		t.Fatal(err)
	}
	if err := <-merged; err != nil {
		t.Fatal(err)
	}
	cfg, _ := h.row("c1")
	if cfg["Host"] != "irc.other" || fmt.Sprint(cfg["Channels"]) != "[#a #new]" {
		t.Errorf("cfg %v", cfg)
	}
}

// An edit liercd refuses saves nothing, and a merge waiting on it lands
// on the old config.
func TestEditDestroyFailsKeepsMerge(t *testing.T) {
	h, a := setup(t)
	testenv.AddConnection(t, h.d, "c1", a, `{"Host":"irc.example","Port":6667,"Nick":"me","Channels":["#a"]}`)
	h.fake.Respond("POST", "/c1/destroy", 500, "boom")
	merged := h.mergeDuring("/c1/destroy", `{"Channels":["#a","#new"]}`)
	err := h.s.Edit(context.Background(), a, "c1", []byte(`{"Host":"irc.other","Port":6667,"Nick":"me"}`))
	userMsg(t, err, "boom")
	if err := <-merged; err != nil {
		t.Fatal(err)
	}
	cfg, _ := h.row("c1")
	if cfg["Host"] != "irc.example" || fmt.Sprint(cfg["Channels"]) != "[#a #new]" {
		t.Errorf("cfg %v", cfg)
	}
}

func TestEditKeepsNick(t *testing.T) {
	h, a := setup(t)
	testenv.AddConnection(t, h.d, "c1", a, `{"Host":"irc.example","Port":6667,"Nick":"me","Channels":[]}`)
	if err := h.s.Edit(context.Background(), a, "c1", []byte(`{"Host":"irc.example","Port":6667,"Nick":"","Alias":"N"}`)); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := h.row("c1"); cfg["Nick"] != "me" {
		t.Errorf("nick = %v", cfg["Nick"])
	}
	h.fake.Respond("POST", "/c1/destroy", 200, "ok")
	if err := h.s.Edit(context.Background(), a, "c1", []byte(`{"Host":"irc.example","Port":6667,"Nick":"new"}`)); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := h.row("c1"); cfg["Nick"] != "new" {
		t.Errorf("nick = %v", cfg["Nick"])
	}
	err := h.s.Edit(context.Background(), a, "c1", []byte(`[]`))
	userMsg(t, err, "Invalid connection settings")
}

func TestSave(t *testing.T) {
	h, a := setup(t)
	testenv.AddConnection(t, h.d, "c1", a, `{"Host":"irc.example","Port":6667,"Nick":"me","Channels":["#a"],"Alias":"Old"}`)
	if err := h.s.Save(context.Background(), a, "c1", []byte(`{"Alias":"Net","Highlight":["x"]}`)); err != nil {
		t.Fatal(err)
	}
	cfg, _ := h.row("c1")
	if cfg["Alias"] != "Net" || fmt.Sprint(cfg["Highlight"]) != "[x]" || cfg["Host"] != "irc.example" || fmt.Sprint(cfg["Channels"]) != "[#a]" {
		t.Errorf("cfg %v", cfg)
	}
	if err := h.s.Save(context.Background(), a, "c1", []byte(`{"Alias":""}`)); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := h.row("c1"); cfg["Alias"] != nil {
		t.Errorf("alias kept: %v", cfg)
	}
	if fmt.Sprint(h.said) != "[update c1 update c1]" || h.notes != 2 || len(h.fake.Calls()) != 0 {
		t.Errorf("said %v notes %d calls %v", h.said, h.notes, h.calls())
	}
	userMsg(t, h.s.Save(context.Background(), a, "c1", []byte(`"x"`)), "Invalid connection settings")
}

func TestDelete(t *testing.T) {
	h, a := setup(t)
	for _, c := range []struct {
		id   string
		code int
	}{{"c1", 200}, {"c2", 404}} {
		testenv.AddConnection(t, h.d, c.id, a, "{}")
		h.fake.Respond("POST", "/"+c.id+"/destroy", c.code, "x")
		if err := h.s.Delete(context.Background(), a, c.id); err != nil {
			t.Errorf("%s: %v", c.id, err)
		}
		if _, ok := h.row(c.id); ok {
			t.Errorf("%s left", c.id)
		}
	}
	if fmt.Sprint(h.said) != "[delete c1 delete c2]" {
		t.Errorf("said %v", h.said)
	}
	testenv.AddConnection(t, h.d, "c3", a, "{}")
	h.fake.Respond("POST", "/c3/destroy", 500, "x\n")
	userMsg(t, h.s.Delete(context.Background(), a, "c3"), "x")
	if _, ok := h.row("c3"); !ok {
		t.Error("c3 deleted after a failed destroy")
	}
}

// serverKey reads saved configs as Clean writes them: Port a JSON number,
// Ssl/SASL JSON booleans. Any other type counts as a change, so the
// connection reconnects rather than keeping stale settings.
func TestServerKey(t *testing.T) {
	k := func(s string) string {
		var m map[string]any
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		d.Decode(&m)
		return serverKey(m)
	}
	// a saved config (json.Number) matches the same config fresh from Clean (int, bool)
	if k(`{"Host":"h","Port":6667,"Ssl":true}`) != serverKey(map[string]any{"Host": "h", "Port": 6667, "Ssl": true}) {
		t.Error("saved and cleaned configs differ")
	}
	if k(`{"Host":"h","Port":"6667"}`) == k(`{"Host":"h","Port":6667}`) {
		t.Error("a string port matched a number")
	}
	if k(`{"Host":"h","Ssl":1}`) == k(`{"Host":"h","Ssl":true}`) {
		t.Error("Ssl 1 matched true")
	}
	if k(`{"Host":"h","SASL":"1"}`) == k(`{"Host":"h","SASL":true}`) {
		t.Error(`SASL "1" matched true`)
	}
	if k(`{"Host":"h","Ssl":false}`) != k(`{"Host":"h"}`) {
		t.Error("a missing Ssl differs from false")
	}
	if k(`{"Host":"h","Port":6667}`) == k(`{"Host":"h","Port":6697}`) {
		t.Error("port ignored")
	}
	if k(`{"Host":" IRC.Example "}`) != k(`{"Host":"irc.example"}`) {
		t.Error("host case differs")
	}
	if k(`{"Host":"h","Nick":"a"}`) == k(`{"Host":"h","Nick":"b"}`) {
		t.Error("nick ignored")
	}
}

// cancelling is a liercd that cancels the caller's request (the browser
// went away) while answering path, then answers code.
func cancelling(t *testing.T, path string, code int, body string, cancel func()) *liercd.Client {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path == path {
			cancel()
			time.Sleep(100 * time.Millisecond)
			w.WriteHeader(code)
			io.WriteString(w, body)
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	cancelCalls = func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), calls...) }
	return &liercd.Client{BaseURL: srv.URL, HTTP: &http.Client{}}
}

var cancelCalls func() []string

// A browser that leaves mid-create mustn't stop the undo: the refused
// create's row is still deleted and announced.
func TestCreateFinishesAfterCancel(t *testing.T) {
	h, a := setup(t)
	old := newID
	newID = func() string { return "fixedid" }
	t.Cleanup(func() { newID = old })
	ctx, cancel := context.WithCancel(context.Background())
	h.s.Liercd = cancelling(t, "/fixedid/create", 400, "nok\n", cancel)
	_, err := h.s.Create(ctx, a, []byte(valid))
	userMsg(t, err, "nok")
	if _, ok := h.row("fixedid"); ok || fmt.Sprint(h.said) != "[create fixedid delete fixedid]" {
		t.Errorf("row left %v, said %v", ok, h.said)
	}
}

// A browser that leaves mid-edit mustn't leave the network destroyed: the
// recreate still happens.
func TestEditFinishesAfterCancel(t *testing.T) {
	h, a := setup(t)
	testenv.AddConnection(t, h.d, "c1", a, `{"Host":"irc.example","Port":6667,"Nick":"me","Channels":[]}`)
	ctx, cancel := context.WithCancel(context.Background())
	h.s.Liercd = cancelling(t, "/c1/destroy", 200, "ok", cancel)
	if err := h.s.Edit(ctx, a, "c1", []byte(`{"Host":"irc.other","Port":6667,"Nick":"me"}`)); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(cancelCalls()) != "[POST /c1/destroy POST /c1/create]" || fmt.Sprint(h.said) != "[update c1]" {
		t.Errorf("calls %v said %v", cancelCalls(), h.said)
	}
}

func TestCreateDefault(t *testing.T) {
	h, a := setup(t)
	cfg := map[string]any{"Host": "bad.example", "Port": 6667, "Nick": "alice", "Channels": []string{"#a"}}
	id, err := h.s.CreateDefault(context.Background(), a, cfg)
	if err != nil || id == "" {
		t.Fatalf("CreateDefault = %q, %v", id, err)
	}
	row, ok := h.row(id)
	if !ok || row["Host"] != "bad.example" {
		t.Errorf("row = %v %v", row, ok)
	}
	var created bool
	h.d.Pool.QueryRow(context.Background(), `SELECT created IS NOT NULL AND updated IS NOT NULL FROM connection WHERE id=$1`, id).Scan(&created)
	if !created || len(h.said) != 0 || fmt.Sprint(h.calls()) != "[POST /"+id+"/create]" || h.notes != 1 {
		t.Errorf("created %v said %v calls %v notes %d", created, h.said, h.calls(), h.notes)
	}

	old := newID
	newID = func() string { return "failid" }
	defer func() { newID = old }()
	h.fake.Respond("POST", "/failid/create", 500, "boom")
	id, err = h.s.CreateDefault(context.Background(), a, cfg)
	if err == nil || id != "failid" {
		t.Errorf("CreateDefault on 500 = %q, %v", id, err)
	}
	if _, ok := h.row("failid"); !ok {
		t.Error("row deleted after liercd failed")
	}
	if len(h.said) != 0 {
		t.Errorf("said %v", h.said)
	}
}
