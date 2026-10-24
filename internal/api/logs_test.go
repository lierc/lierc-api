package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

type seedRow struct {
	channel, command, text, sender string
	highlight, self                bool
	age                            string // postgres interval, default "0"
	user                           string
}

// seed inserts a log row for conn and returns its id.
func (e *connEnv) seed(conn string, r seedRow) int {
	e.t.Helper()
	if r.command == "" {
		r.command = "PRIVMSG"
	}
	if r.age == "" {
		r.age = "0 seconds"
	}
	if r.user == "" {
		r.user = e.id
	}
	msg, _ := json.Marshal(map[string]any{
		"Prefix": map[string]any{"Name": r.sender}, "Command": r.command, "Params": []string{r.channel, r.text},
	})
	var id int
	err := e.db.Pool.QueryRow(context.Background(), `
		INSERT INTO log (connection, "user", channel, command, highlight, time, message, self)
		VALUES ($1,$2,$3,$4,$5,NOW() - $6::interval,$7,$8) RETURNING id`,
		conn, r.user, r.channel, r.command, r.highlight, r.age, string(msg), r.self).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *connEnv) get(target string) *httptest.ResponseRecorder { return e.raw("GET", target, "") }

type entry struct {
	MessageId    int
	Message      map[string]any
	ConnectionId string
	Self         *bool
	Highlight    bool
}

func decodeEntries(t *testing.T, rec *httptest.ResponseRecorder) []entry {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out []entry
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	return out
}

func TestLogsLimitClamp(t *testing.T) {
	e := setupConn(t)
	c := e.create(connBody)
	e.db.Pool.Exec(context.Background(), `
		INSERT INTO log (connection, "user", channel, command, time, message)
		SELECT $1,$2,'#a','PRIVMSG',NOW(),'{"Params":["#a","x"]}' FROM generate_series(1,200)`, c, e.id)
	for q, want := range map[string]int{"": 50, "?limit=500": 150, "?limit=0": 50, "?limit=7": 7} {
		got := decodeEntries(t, e.get("/connection/"+c+"/channel/%23a/events"+q))
		if len(got) != want {
			t.Errorf("%q: %d rows, want %d", q, len(got), want)
		}
	}
}

func TestLogsOrderAndShape(t *testing.T) {
	e := setupConn(t)
	c := e.create(connBody)
	a := e.seed(c, seedRow{channel: "#a", text: "one"})
	b := e.seed(c, seedRow{channel: "#a", text: "two", highlight: true})
	rec := e.get("/connection/" + c + "/channel/%23a/events")
	want := fmt.Sprintf(`[{"MessageId":%d,"Message":{"Command":"PRIVMSG","Params":["#a","two"],"Prefix":{"Name":""}},"ConnectionId":"%s","Highlight":true},`+
		`{"MessageId":%d,"Message":{"Command":"PRIVMSG","Params":["#a","one"],"Prefix":{"Name":""}},"ConnectionId":"%s","Highlight":false}]`, b, c, a, c)
	expectJSON(t, rec, want)
	if ct := rec.Header().Get("Content-Type"); ct != "application/javascript;charset=utf-8" {
		t.Fatal(ct)
	}
}

func TestLogsBefore(t *testing.T) {
	e := setupConn(t)
	c := e.create(connBody)
	a := e.seed(c, seedRow{channel: "#a", text: "one"})
	b := e.seed(c, seedRow{channel: "#a", text: "two"})
	e.seed(c, seedRow{channel: "#a", text: "three"})
	got := decodeEntries(t, e.get(fmt.Sprintf("/connection/%s/channel/%%23a/events/%d", c, b)))
	if len(got) != 1 || got[0].MessageId != a {
		t.Fatalf("%+v", got)
	}
}

func TestChannelLowercaseUnicode(t *testing.T) {
	e := setupConn(t)
	c := e.create(connBody)
	e.seed(c, seedRow{channel: "#café", text: "x"})
	if got := decodeEntries(t, e.get("/connection/"+c+"/channel/%23Caf%C3%89/events")); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestChannelList(t *testing.T) {
	e := setupConn(t)
	c := e.create(connBody)
	e.seed(c, seedRow{channel: "#a"})
	e.seed(c, seedRow{channel: "#b"})
	e.seed(c, seedRow{channel: "#a"})
	expectJSON(t, e.get("/connection/"+c+"/channel"), `["#a","#b"]`)
	other := e.create(connBody)
	expectJSON(t, e.get("/connection/"+other+"/channel"), `[]`)
}

func sseEvents(t *testing.T, rec *httptest.ResponseRecorder) []entry {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream;charset=utf-8" {
		t.Fatalf("content type %q", ct)
	}
	var out []entry
	for _, blk := range strings.Split(strings.TrimSuffix(rec.Body.String(), "\n\n"), "\n\n") {
		if blk == "" {
			continue
		}
		name, data, ok := strings.Cut(blk, "\n")
		if name != "event: log" || !ok || !strings.HasPrefix(data, "data: ") {
			t.Fatalf("bad event %q", blk)
		}
		var en entry
		if err := json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &en); err != nil {
			t.Fatal(err)
		}
		out = append(out, en)
	}
	return out
}

func TestDateStream(t *testing.T) {
	e := setupConn(t)
	c := e.create(connBody)
	e.seed(c, seedRow{channel: "#a", text: "old", age: "10 days"})
	in1 := e.seed(c, seedRow{channel: "#a", text: "hello world", sender: "bob", age: "2 days"})
	in2 := e.seed(c, seedRow{channel: "#a", text: "goodbye", sender: "carol", age: "2 days"})
	e.seed(c, seedRow{channel: "#a", text: "new"})
	var from, to string
	e.db.Pool.QueryRow(context.Background(), `SELECT (NOW()-'3 days'::interval)::date::text, (NOW()-'2 days'::interval)::date::text`).Scan(&from, &to)
	base := fmt.Sprintf("/connection/%s/channel/%%23A/date/%s/%s", c, from, to)

	rec := e.get(base)
	got := sseEvents(t, rec)
	if len(got) != 2 || got[0].MessageId != in1 || got[1].MessageId != in2 || got[0].ConnectionId != c {
		t.Fatalf("%+v", got)
	}
	if !strings.HasPrefix(rec.Body.String(), "event: log\ndata: {") || !strings.HasSuffix(rec.Body.String(), "}\n\n") {
		t.Fatalf("framing %q", rec.Body)
	}
	for q, want := range map[string][]int{
		"?text=hello":              {in1},
		"?sender=carol":            {in2},
		"?text=hello&sender=bob":   {in1},
		"?text=hello&sender=carol": nil,
		"?sender=nobody":           nil,
	} {
		got := sseEvents(t, e.get(base+q))
		if len(got) != len(want) {
			t.Errorf("%s: %d events", q, len(got))
			continue
		}
		for i := range want {
			if got[i].MessageId != want[i] {
				t.Errorf("%s: %+v", q, got)
			}
		}
	}
}

func TestLast(t *testing.T) {
	e := setupConn(t)
	c := e.create(connBody)
	e.seed(c, seedRow{channel: "#a", text: "ancient apple", age: "8 days"})
	e.seed(c, seedRow{channel: "#a", text: "apple join", command: "JOIN"})
	e.seed(c, seedRow{channel: "#a", text: "banana"})
	for i := 0; i < 25; i++ {
		e.seed(c, seedRow{channel: "#a", text: fmt.Sprintf("apple pie %d", i)})
	}
	base := "/connection/" + c + "/channel/%23a/last?query=apple"
	got := decodeEntries(t, e.get(base))
	if len(got) != 5 {
		t.Fatalf("default limit: %d", len(got))
	}
	if txt := got[0].Message["Params"].([]any)[1]; txt != "\x02apple\x02 pie 24" {
		t.Fatalf("headline %q", txt)
	}
	if n := len(decodeEntries(t, e.get(base+"&limit=100"))); n != 20 {
		t.Fatalf("max limit: %d", n)
	}
	if n := len(decodeEntries(t, e.get(base+"&limit=0"))); n != 5 {
		t.Fatalf("limit 0: %d", n)
	}
	if n := len(decodeEntries(t, e.get("/connection/"+c+"/channel/%23a/last?query=banana"))); n != 1 {
		t.Fatalf("banana: %d", n)
	}
}

func TestSetSeen(t *testing.T) {
	e := setupConn(t)
	c := e.create(connBody)
	for _, pos := range []string{"5", "9"} {
		if rec := e.raw("POST", "/connection/"+c+"/channel/%23A/seen", pos); rec.Code != 204 || rec.Body.Len() != 0 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	expectJSON(t, e.get("/seen"), fmt.Sprintf(`[{"connection":"%s","channel":"#a","message_id":9}]`, c))
	if rec := e.raw("POST", "/connection/"+c+"/channel/%23A/seen", "abc"); rec.Code != 400 {
		t.Fatalf("bad position: %d", rec.Code)
	}
}

func TestSeen(t *testing.T) {
	e := setupConn(t)
	expectJSON(t, e.get("/seen"), `[]`)
}

func TestMissed(t *testing.T) {
	e := setupConn(t)
	c := e.create(connBody)
	first := e.seed(c, seedRow{channel: "#a"})
	e.seed(c, seedRow{channel: "#a"})
	e.seed(c, seedRow{channel: "#a", highlight: true})
	e.seed(c, seedRow{channel: "#a", command: "JOIN"})
	e.seed(c, seedRow{channel: "#b"})
	expectJSON(t, e.get(fmt.Sprintf("/missed?%s-%%23a=%d", c, first)),
		fmt.Sprintf(`{"%s":{"#a":{"events":1,"highlights":1,"messages":2}}}`, c))
	expectJSON(t, e.get(fmt.Sprintf("/missed?%s-%%23a=0&%s-%%23b=0", c, c)),
		fmt.Sprintf(`{"%s":{"#a":{"events":1,"highlights":1,"messages":3},"#b":{"events":0,"highlights":0,"messages":1}}}`, c))
	expectJSON(t, e.get("/missed"), `{}`)

	// another user's connection
	other, _, err := e.db.AddUser(context.Background(), "bob", "bob@example.com", "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.SaveConnection(context.Background(), "theirs", other, []byte(connBody)); err != nil {
		t.Fatal(err)
	}
	rec := e.get("/missed?theirs-%23a=0")
	if rec.Code != 400 || !strings.HasPrefix(rec.Body.String(), `{"status":"error","error":"Invalid connection (theirs)`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestLog250(t *testing.T) {
	e := setupConn(t)
	c := e.create(connBody)
	insert := func(n int) {
		_, err := e.db.Pool.Exec(context.Background(), `
			INSERT INTO log (connection, "user", channel, command, time, message, self)
			SELECT $1,$2,'#a','PRIVMSG',NOW(),'{"Params":["#a","x"]}', true FROM generate_series(1,$3)`, c, e.id, n)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert(250)
	got := decodeEntries(t, e.get("/log/0"))
	if len(got) != 250 {
		t.Fatalf("%d rows", len(got))
	}
	if got[0].MessageId >= got[249].MessageId {
		t.Fatal("not oldest first")
	}
	if got[0].Self == nil || !*got[0].Self {
		t.Fatal("Self missing")
	}
	insert(1)
	rec := e.get("/log/0")
	expectConnError(t, rec, "Too many messages!")
}

func TestHighlight(t *testing.T) {
	e := setupConn(t)
	c := e.create(connBody)
	e.seed(c, seedRow{channel: "#a", text: "quiet"})
	a := e.seed(c, seedRow{channel: "#a", text: "hl1", highlight: true})
	b := e.seed(c, seedRow{channel: "#b", text: "hl2", highlight: true, self: true})
	e.seed(c, seedRow{channel: "#b", text: "other user's", highlight: true, user: "nobody"})
	got := decodeEntries(t, e.get("/highlight"))
	if len(got) != 2 || got[0].MessageId != b || got[1].MessageId != a {
		t.Fatalf("%+v", got)
	}
	if got[0].Self == nil || !*got[0].Self || !got[0].Highlight || *got[1].Self {
		t.Fatalf("%+v", got)
	}
	got = decodeEntries(t, e.get(fmt.Sprintf("/highlight/%d", b)))
	if len(got) != 1 || got[0].MessageId != a {
		t.Fatalf("%+v", got)
	}
	if n := len(decodeEntries(t, e.get("/highlight?limit=1"))); n != 1 {
		t.Fatal(n)
	}
}

func TestIgnoreCreateListDelete(t *testing.T) {
	e := setupConn(t)
	c := e.create(connBody)
	base := "/connection/" + c + "/channel/%23Café/ignore"
	expectJSON(t, e.raw("POST", base+"?from=Spammer", ""), `{"status":"ok"}`)
	expectJSON(t, e.get("/ignore"), fmt.Sprintf(`[{"channel":"#café","connection":"%s","from":"Spammer"}]`, c))
	if rec := e.raw("POST", base+"?from=Spammer", ""); rec.Code != 400 {
		t.Fatalf("duplicate: %d", rec.Code)
	}
	// the path segment is ignored; the query parameter selects the row
	if rec := e.raw("DELETE", base+"/Other?from=Nobody", ""); rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	expectJSON(t, e.get("/ignore"), fmt.Sprintf(`[{"channel":"#café","connection":"%s","from":"Spammer"}]`, c))
	if rec := e.raw("DELETE", base+"/Spammer?from=Spammer", ""); rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatal(rec.Code)
	}
	expectJSON(t, e.get("/ignore"), `[]`)
}

func TestIgnoreListColumns(t *testing.T) {
	e := setupConn(t)
	c := e.create(connBody)
	e.raw("POST", "/connection/"+c+"/channel/%23a/ignore?from=x", "")
	var rows []map[string]any
	json.Unmarshal(e.get("/ignore").Body.Bytes(), &rows)
	if len(rows) != 1 || len(rows[0]) != 3 {
		t.Fatalf("%v", rows)
	}
	// every column of the ignore table
	var n int
	e.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.columns WHERE table_name='ignore'`).Scan(&n)
	if n != 3 {
		t.Fatalf("ignore table has %d columns", n)
	}
}

// Perl binds the raw limit string unless it numifies above the max
// (Channel.pm logs: min($limit || 50, 150)), so Postgres rejects junk.
func TestLogsLimitPerlNumification(t *testing.T) {
	e := setupConn(t)
	c := e.create(connBody)
	e.db.Pool.Exec(context.Background(), `
		INSERT INTO log (connection, "user", channel, command, time, message)
		SELECT $1,$2,'#a','PRIVMSG',NOW(),'{"Params":["#a","x"]}' FROM generate_series(1,200)`, c, e.id)
	base := "/connection/" + c + "/channel/%23a/events"
	for q, want := range map[string]int{"?limit=": 50, "?limit=0": 50, "?limit=200abc": 150, "?limit=1e1": -1, "?limit=abc": -1, "?limit=1.5": -1, "?limit=60abc": -1, "?limit=%207": 7} {
		rec := e.get(base + q)
		if want < 0 {
			if rec.Code != 400 || rec.Body.String() != `{"status":"error","error":null}` {
				t.Errorf("%q: %d %s", q, rec.Code, rec.Body)
			}
			continue
		}
		if got := decodeEntries(t, rec); len(got) != want {
			t.Errorf("%q: %d rows, want %d", q, len(got), want)
		}
	}
}
