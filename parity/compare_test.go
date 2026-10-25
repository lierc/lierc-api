package parity

import (
	"strings"
	"testing"
)

func jsonResult(status int, body string) Result {
	return Result{Status: status, ContentType: "application/javascript;charset=utf-8", Body: []byte(body)}
}

func TestCompare(t *testing.T) {
	hashRef := pattern("$.ref", `^HASH\(0x[0-9a-f]+\)$`, "")
	cases := []struct {
		name       string
		perl, goR  Result
		allows     []Allow
		wantDiffs  int
		wantSubstr string
	}{
		{"key order ignored", jsonResult(200, `{"a":1,"b":[true,null]}`), jsonResult(200, `{"b":[true,null],"a":1}`), nil, 0, ""},
		{"number vs string", jsonResult(200, `{"MessageId":1}`), jsonResult(200, `{"MessageId":"1"}`), nil, 1, "type: perl number"},
		{"bool vs number", jsonResult(200, `{"Self":true}`), jsonResult(200, `{"Self":1}`), nil, 1, "type: perl bool"},
		{"1 equals 1.0", jsonResult(200, `[1]`), jsonResult(200, `[1.0]`), nil, 0, ""},
		{"value differs", jsonResult(200, `{"a":"x"}`), jsonResult(200, `{"a":"y"}`), nil, 1, `$.a: perl "x"`},
		{"missing key", jsonResult(200, `{"a":1,"b":2}`), jsonResult(200, `{"a":1}`), nil, 1, `key "b" only in perl`},
		{"array length", jsonResult(200, `[1,2]`), jsonResult(200, `[1]`), nil, 1, "length"},
		{"status", jsonResult(200, `{}`), jsonResult(401, `{}`), nil, 1, "status: perl 200, go 401"},
		{"content type", jsonResult(200, `{}`), Result{200, "text/plain", []byte(`{}`)}, nil, 1, "Content-Type"},
		{"empty bodies", Result{Status: 204}, Result{Status: 204}, nil, 0, ""},
		{"empty vs body", Result{Status: 204}, Result{Status: 204, Body: []byte("x")}, nil, 1, "body"},
		{"any value", jsonResult(200, `{"token":"a","extra":["b","c"]}`), jsonResult(200, `{"token":"x","extra":["y","z"]}`),
			[]Allow{anyValue("$.token", ""), anyValue("$.extra[*]", "")}, 0, ""},
		{"any value keeps type", jsonResult(400, `{"error":null}`), jsonResult(400, `{"error":"boom"}`),
			[]Allow{anyValue("$.error", "")}, 1, "type: perl null"},
		{"same format", jsonResult(200, `[{"time":"2026-10-23 10:11:12.5"}]`), jsonResult(200, `[{"time":"2026-10-23 10:11:13.123456"}]`),
			[]Allow{sameFormat("$[*].time", "")}, 0, ""},
		{"format differs", jsonResult(200, `[{"time":"2026-10-23 10:11:12.5"}]`), jsonResult(200, `[{"time":"2026-10-23T10:11:12.5Z"}]`),
			[]Allow{sameFormat("$[*].time", "")}, 1, "format"},
		{"pattern", jsonResult(200, `{"ref":"HASH(0x55d1)"}`), jsonResult(200, `{"ref":"HASH(0xab)"}`),
			[]Allow{hashRef}, 0, ""},
		{"pattern fails", jsonResult(200, `{"ref":"HASH(0x55d1)"}`), jsonResult(200, `{"ref":"abc"}`),
			[]Allow{hashRef}, 1, `go "abc" does not match`},
		{"wildcard member", jsonResult(200, `{"x":{"n":1},"y":{"n":2}}`), jsonResult(200, `{"x":{"n":3},"y":{"n":4}}`),
			[]Allow{anyValue("$.*.n", "")}, 0, ""},
		{"event stream", Result{200, "text/event-stream;charset=utf-8", []byte("event: log\ndata: {\"a\":1,\"b\":2}\n\n")},
			Result{200, "text/event-stream;charset=utf-8", []byte("event: log\ndata: {\"b\":2,\"a\":1}\n\n")}, nil, 0, ""},
		{"event stream differs", Result{200, "text/event-stream;charset=utf-8", []byte("event: log\ndata: {\"a\":1}\n\n")},
			Result{200, "text/event-stream;charset=utf-8", []byte("event: log\ndata: {\"a\":2}\n\n")}, nil, 1, "$[0].data.a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			diffs := Compare(c.perl, c.goR, c.allows)
			if len(diffs) != c.wantDiffs {
				t.Fatalf("diffs = %q, want %d", diffs, c.wantDiffs)
			}
			if c.wantSubstr != "" && !strings.Contains(strings.Join(diffs, "\n"), c.wantSubstr) {
				t.Fatalf("diffs = %q, want one containing %q", diffs, c.wantSubstr)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	got := string(Normalize([]byte(`{"abcd1234":{"id":"abcd1234","user":"u1","x":"Invalid connection (abcd1234)"}}`),
		map[string]string{"conn": "abcd1234", "short": "u1"}))
	want := `{"{{conn}}":{"id":"{{conn}}","user":"u1","x":"Invalid connection ({{conn}})"}}`
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	// Longer values first, so a value containing another is replaced whole.
	got = string(Normalize([]byte(`"abcdef abcd"`), map[string]string{"a": "abcd", "b": "abcdef"}))
	if got != `"{{b}} {{a}}"` {
		t.Fatalf("got %s", got)
	}
}

func TestParseSSE(t *testing.T) {
	evs := ParseSSE([]byte(":    \nevent: ping\ndata: 1.5\n\nevent: irc\ndata: {}\nid: 7\n\n"))
	if len(evs) != 3 || evs[0].Comment != ":    " || evs[1].Event != "ping" || evs[1].Data != "1.5" ||
		evs[2].Event != "irc" || evs[2].ID != "7" {
		t.Fatalf("%+v", evs)
	}
}
