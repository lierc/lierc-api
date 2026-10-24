// Package parity runs the same scenarios against the Perl and Go servers and
// diffs the results. The scenarios and the comparison live in untagged files
// so `go vet ./...` and the unit tests cover them; the runner that needs the
// compose stack is parity_test.go, behind the `parity` build tag.
package parity

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Scenario is a sequence of steps. Each scenario runs twice from an empty
// database, once against Perl and once against Go, and every step's result is
// compared between the two runs.
type Scenario struct {
	Name  string
	Steps []Step
}

// Step is one request, database query or fake inspection. Strings may hold
// {{var}} placeholders, expanded with the values captured so far in the same
// run. Setup (SQL, CaptureSQL, Liercd) happens before the step's action.
type Step struct {
	Name string
	// As names the cookie jar: "" is "alice", any other name is its own
	// user, and "anon" sends no cookies. Each server run has its own jars.
	As string

	// The request, when Method is set.
	Method, Path string
	Header       map[string]string
	Body         string

	// SQL statements to run first, e.g. to seed log rows.
	SQL []string
	// CaptureSQL saves the single value each query returns as a variable.
	CaptureSQL map[string]string
	// Liercd scripts fake liercd responses before the request.
	Liercd []LiercdResponse

	// Query, instead of a request: its rows, as JSON objects, are compared.
	Query string
	// Mail, instead of a request: the mail the fake SMTP server accepted in
	// this run is compared (sender, recipients, subject and decoded body).
	Mail bool
	// LiercdCalls, instead of a request: the calls fake liercd received in
	// this run are compared.
	LiercdCalls bool

	// Capture saves values from the JSON response by path ("$.id",
	// "$.extra[0]") as variables for later steps. Captured values are also
	// replaced by "{{var}}" in both servers' results before comparing.
	Capture map[string]string
	// Allow lists the values that may legitimately differ, with the reason.
	Allow []Allow
}

// LiercdResponse forces fake liercd's answer for Method and Path. Path may be
// "/*/<action>" to match every connection id.
type LiercdResponse struct {
	Method, Path string
	Code         int
	Body         string
}

// Label names the step in failure messages.
func (s Step) Label() string {
	switch {
	case s.Name != "":
		return s.Name
	case s.Query != "":
		return "query: " + s.Query
	case s.Mail:
		return "mail"
	case s.LiercdCalls:
		return "liercd calls"
	case s.Method != "":
		l := s.Method + " " + s.Path
		if s.As != "" {
			l += " as " + s.As
		}
		return l
	}
	return "setup"
}

// Expand replaces {{var}} placeholders with vars' values. Unknown
// placeholders are left as they are.
func Expand(s string, vars map[string]string) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	return placeholder.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := vars[m[2:len(m)-2]]; ok {
			return v
		}
		return m
	})
}

var placeholder = regexp.MustCompile(`\{\{[A-Za-z0-9_]+\}\}`)

// Lookup returns the value at path ("$", "$.a.b", "$.list[2].x") in v.
func Lookup(v any, path string) (any, bool) {
	rest, ok := strings.CutPrefix(path, "$")
	if !ok {
		return nil, false
	}
	for rest != "" {
		switch rest[0] {
		case '.':
			end := strings.IndexAny(rest[1:], ".[")
			if end < 0 {
				end = len(rest) - 1
			}
			m, ok := v.(map[string]any)
			if !ok {
				return nil, false
			}
			if v, ok = m[rest[1:end+1]]; !ok {
				return nil, false
			}
			rest = rest[end+1:]
		case '[':
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, false
			}
			i, err := strconv.Atoi(rest[1:end])
			a, ok := v.([]any)
			if err != nil || !ok || i < 0 || i >= len(a) {
				return nil, false
			}
			v = a[i]
			rest = rest[end+1:]
		default:
			return nil, false
		}
	}
	return v, true
}

// CaptureString renders a captured JSON value as the text used in
// placeholders: strings as themselves, anything else as its JSON.
func CaptureString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Step builders.

func req(method, path string) Step { return Step{Method: method, Path: path} }
func get(path string) Step         { return req("GET", path) }
func post(path string) Step        { return req("POST", path) }

func (s Step) as(jar string) Step { s.As = jar; return s }

func (s Step) named(name string) Step { s.Name = name; return s }

func (s Step) header(k, v string) Step {
	h := map[string]string{}
	for hk, hv := range s.Header {
		h[hk] = hv
	}
	h[k] = v
	s.Header = h
	return s
}

// form sends kv pairs as an urlencoded body.
func (s Step) form(kv ...string) Step {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	s.Body = v.Encode()
	return s.header("Content-Type", "application/x-www-form-urlencoded")
}

// raw sends body as text/plain, which Plack does not parse into parameters.
func (s Step) raw(body string) Step {
	s.Body = body
	return s.header("Content-Type", "text/plain")
}

func (s Step) json(body string) Step {
	s.Body = body
	return s.header("Content-Type", "application/json")
}

func (s Step) capture(name, path string) Step {
	c := map[string]string{name: path}
	for k, v := range s.Capture {
		c[k] = v
	}
	s.Capture = c
	return s
}

func (s Step) captureSQL(name, sql string) Step {
	c := map[string]string{name: sql}
	for k, v := range s.CaptureSQL {
		c[k] = v
	}
	s.CaptureSQL = c
	return s
}

func (s Step) allow(a ...Allow) Step {
	s.Allow = append(append([]Allow(nil), s.Allow...), a...)
	return s
}

func (s Step) liercd(method, path string, code int, body string) Step {
	s.Liercd = append(append([]LiercdResponse(nil), s.Liercd...), LiercdResponse{method, path, code, body})
	return s
}

func query(sql string) Step { return Step{Query: sql} }

func seed(sql ...string) Step { return Step{Name: "seed", SQL: sql} }

// register signs up user (password "hunter2") on jar user, or on "alice"
// for "", capturing the new id as {{<jar>_id}} ({{user}} for alice).
func register(jar string) Step {
	name, idVar := jar, jar+"_id"
	if jar == "" {
		name, idVar = "alice", "user"
	}
	return post("/register").as(jar).
		form("username", name, "email", name+"@example.com", "pass", "hunter2").
		capture(idVar, "$.id")
}

// createConn creates a connection for jar and captures its id as v.
func createConn(jar, v string) Step {
	return post("/connection").as(jar).json(connConfig).capture(v, "$.id")
}

const verifyToken = `SELECT verify_token FROM "user" WHERE username='alice'`

const connConfig = `{"Host":"irc.example.com","Port":6667,"Ssl":true,"Nick":"alice",` +
	`"Alias":"Example","Channels":["#a","#b"],"Pass":null,"Ratio":1.5,"Name":"Ünïcode é \/"}`

var (
	// The owner check's message interpolates the request object
	// (Dispatch.pm), and the address varies between requests.
	invalidConn = pattern("$.error",
		`^Invalid connection id 'API::Request=HASH\(0x[0-9a-f]+\)->captures->\{id\}'$`,
		"Perl interpolates $req into the message; the address varies")
	dbError = libraryError("Postgres error without DETAIL: Perl sends null; text never compared")
	// DBD::Pg prints timestamp columns in Postgres text form.
	timestampFormat = "timestamps are taken at run time; their text format must match"
)

func unauthorizedRoutes() []Step {
	routes := [][2]string{
		{"GET", "/health"}, {"GET", "/auth"}, {"GET", "/token"}, {"GET", "/verify?x"},
		{"GET", "/log/0"}, {"GET", "/missed"}, {"GET", "/seen"},
		{"GET", "/highlight"}, {"GET", "/highlight/1"},
		{"GET", "/preference"}, {"GET", "/preference/x"}, {"POST", "/preference/x"},
		{"GET", "/unsubscribe"},
		{"GET", "/connection"}, {"POST", "/connection"}, {"GET", "/connection/c1"},
		{"PUT", "/connection/c1"}, {"DELETE", "/connection/c1"}, {"POST", "/connection/c1"},
		{"DELETE", "/connection/c1/nick/bob"}, {"POST", "/connection/c1/nick/bob"},
		{"GET", "/privates"},
		{"GET", "/connection/c1/channel/%23a/date/2020-01-01/2020-01-02"},
		{"GET", "/connection/c1/channel/%23a/events"},
		{"GET", "/connection/c1/channel/%23a/events/5"},
		{"POST", "/connection/c1/channel/%23a/seen"},
		{"GET", "/connection/c1/channel/%23a/last"},
		{"GET", "/connection/c1/channel"},
		{"POST", "/connection/c1/channel/%23a/ignore"},
		{"DELETE", "/connection/c1/channel/%23a/ignore/bob"},
		{"GET", "/ignore"},
		{"GET", "/notification/web_push_keys"}, {"GET", "/notification/web_push"},
		{"POST", "/notification/web_push"}, {"DELETE", "/notification/web_push/https://x/y"},
		{"GET", "/notification/apn/config"}, {"POST", "/notification/apn/device"},
		{"POST", "/image"}, {"GET", "/image"}, {"DELETE", "/image/https://i.imgur.com/x.png"},
		{"GET", "/nope"}, {"PATCH", "/auth"},
	}
	steps := make([]Step, len(routes))
	for i, r := range routes {
		steps[i] = req(r[0], r[1]).as("anon")
	}
	return steps
}

// Scenarios is the parity suite. Every route in API::Routes has at least one
// success case and one error case (the "unauthenticated" scenario is the
// error case for routes that cannot otherwise fail).
var Scenarios = []Scenario{
	{
		Name:  "unauthenticated",
		Steps: unauthorizedRoutes(),
	},
	{
		Name: "auth",
		Steps: []Step{
			register(""),
			get("/auth"),
			get("/health"),
			post("/auth").as("anon").form("email", "alice@example.com", "pass", "wrong"),
			post("/auth").as("fresh").form("email", "alice@example.com", "pass", "hunter2"),
			get("/auth").as("fresh"),
			post("/auth").as("byname").form("email", "alice", "pass", "hunter2"),
			get("/token").allow(
				anyValue("$.token", "generated token"),
				anyValue("$.extra[*]", "generated tokens"),
			),
			get("/logout"),
			get("/auth").named("GET /auth after logout"),
			post("/logout").as("anon"),
			req("DELETE", "/logout").as("fresh"),
			get("/auth").as("fresh").named("GET /auth after DELETE /logout"),
			get("/nope").as("byname").named("unknown path, logged in"),
			req("PATCH", "/auth").as("byname").named("method mismatch, logged in"),
			req("POST", "/health").as("byname").named("POST /health, logged in"),
		},
	},
	{
		Name: "register errors",
		Steps: []Step{
			post("/register").as("anon").form("email", "a@example.com", "pass", "x"),
			post("/register").as("anon").form("username", "a", "email", "  ", "pass", "x"),
			post("/register").as("anon").form("username", "a", "email", "a@example.com"),
			post("/register").as("anon").form("username", "a", "email", "not-an-email", "pass", "x"),
			register(""),
			// Constraint errors: Perl sends the DBD DETAIL line.
			post("/register").as("dup").form("username", "other", "email", "alice@example.com", "pass", "x").
				named("duplicate email"),
			post("/register").as("dup").form("username", "alice", "email", "other@example.com", "pass", "x").
				named("duplicate username"),
			query(`SELECT username, email, verified FROM "user" ORDER BY username`),
			// The registration email: sender, recipient, subject and body.
			Step{Mail: true}.captureSQL("verify", verifyToken),
		},
	},
	{
		Name: "verify",
		Steps: []Step{
			register(""),
			// Auth.pm: selectcol_arrayref returns [] for an unknown token,
			// which is true, so Perl may answer ok here.
			get("/verify?nope").named("verify unknown token"),
			query(`SELECT username, verified FROM "user"`),
			get("/verify?{{verify}}").captureSQL("verify", verifyToken).named("verify token"),
			query(`SELECT username, verified FROM "user"`),
			get("/verify?{{verify}}").as("anon").named("verify token, logged out"),
		},
	},
	{
		Name: "connection",
		Steps: []Step{
			register(""),
			post("/connection").json(`{"Host":"irc.example.com","Port":6667}`),
			post("/connection").json(`{"Host":"irc.example.com","Port":6667,"Nick":"  "}`),
			post("/connection").json(`{`).allow(libraryError("JSON::XS parse error text")),
			post("/connection").json(`[1]`).allow(libraryError("Perl dies 'Not a HASH reference'")),
			createConn("", "conn"),
			// Config is stored raw and re-encoded by Perl (decode_json then
			// encode_json); Go passes it through. Compared by meaning.
			get("/connection"),
			get("/connection/{{conn}}"),
			post("/connection/{{conn}}").raw("PRIVMSG #a :hi").named("send without token"),
			get("/token").capture("token", "$.token").allow(
				anyValue("$.token", "generated token"),
				anyValue("$.extra[*]", "generated tokens"),
			),
			post("/connection/{{conn}}").raw("PRIVMSG #a :hi").header("lierc-token", "{{token}}").
				liercd("POST", "/{{conn}}/raw", 200, "{}").
				capture("token2", "$.token").
				allow(anyValue("$.token", "generated token")).
				named("send with token"),
			post("/connection/{{conn}}").raw("PRIVMSG #a :hi").header("lierc-token", "{{token}}").
				named("send with a used token"),
			post("/connection/{{conn}}").raw("PRIVMSG #a :hi").header("lierc-token", "{{token2}}").
				liercd("POST", "/{{conn}}/raw", 500, "raw failed").
				named("send, liercd fails"),
			req("PUT", "/connection/{{conn}}").json(`{"Host":"irc.other.net","Port":"6697","Nick":"alice2"}`).
				liercd("POST", "/{{conn}}/destroy", 200, "{}").
				named("edit"),
			get("/connection/{{conn}}").named("show after edit"),
			// Connection.pm edit ignores the results of delete and create,
			// so a failed destroy still answers ok and keeps the old config.
			req("PUT", "/connection/{{conn}}").json(`{"Host":"irc.third.net","Port":1,"Nick":"x"}`).
				liercd("POST", "/{{conn}}/destroy", 500, "destroy failed").
				named("edit, liercd destroy fails"),
			get("/connection/{{conn}}").named("show after failed edit"),
			req("DELETE", "/connection/{{conn}}").named("delete, liercd fails"),
			req("DELETE", "/connection/{{conn}}").liercd("POST", "/{{conn}}/destroy", 200, "{}").
				named("delete"),
			get("/connection").named("list after delete"),
			{LiercdCalls: true},
			// Last: the failed create's id is generated and never returned.
			post("/connection").json(connConfig).liercd("POST", "/*/create", 500, "create failed").
				named("create, liercd fails"),
			query(`SELECT count(*) AS n FROM connection`),
		},
	},
	{
		Name: "ownership",
		Steps: []Step{
			register(""),
			createConn("", "conn"),
			register("bob"),
			get("/connection/{{conn}}").as("bob").allow(invalidConn),
			req("DELETE", "/connection/{{conn}}").as("bob").allow(invalidConn),
			req("PUT", "/connection/{{conn}}").as("bob").json(connConfig).allow(invalidConn),
			post("/connection/{{conn}}/nick/carol").as("bob").allow(invalidConn),
			get("/connection/{{conn}}/channel").as("bob").allow(invalidConn),
			post("/connection/{{conn}}/channel/%23a/seen").as("bob").raw("1").allow(invalidConn),
			get("/connection/nope").allow(invalidConn).named("unknown connection id"),
			// A method mismatch on an owned path.
			req("PATCH", "/connection/{{conn}}").named("PATCH owned connection"),
			req("PATCH", "/connection/{{conn}}").as("bob").named("PATCH someone else's connection"),
			// Dispatch.pm skips the owner check for a false id such as "0".
			get("/connection/0").allow(libraryError("decode_json(undef) error text")),
			query(`SELECT count(*) AS n FROM connection`),
		},
	},
	{
		Name: "privates",
		Steps: []Step{
			register(""),
			createConn("", "conn"),
			get("/privates").named("no privates"),
			post("/connection/{{conn}}/nick/bob"),
			post("/connection/{{conn}}/nick/zo%C3%AB"),
			get("/privates").allow(sameFormat("$[*].time", timestampFormat)),
			post("/connection/{{conn}}/nick/bob").named("touch bob"),
			get("/privates").allow(sameFormat("$[*].time", timestampFormat)).named("privates after touch"),
			req("DELETE", "/connection/{{conn}}/nick/bob"),
			req("DELETE", "/connection/{{conn}}/nick/nobody"),
			get("/privates").allow(sameFormat("$[*].time", timestampFormat)).named("privates after delete"),
			post("/connection/{{conn}}/nick/" + strings.Repeat("n", 40)).allow(dbError).named("nick too long"),
			post("/connection/nope/nick/bob").allow(invalidConn),
		},
	},
	{
		Name: "logs",
		Steps: append([]Step{
			register(""),
			createConn("", "conn"),
			register("bob"),
			createConn("bob", "bob_conn"),
			seed(seedLog...),
		}, logSteps()...),
	},
	{
		Name: "preferences",
		Steps: []Step{
			register(""),
			get("/preference").named("no preferences"),
			get("/preference/theme").named("missing preference"),
			post("/preference/theme").raw("dárk"),
			get("/preference/theme"),
			post("/preference/theme").raw("light").named("update preference"),
			post("/preference/other").raw("1"),
			get("/preference"),
			get("/unsubscribe"),
			get("/preference/email"),
			post("/preference/theme").raw(strings.Repeat("v", 600)).allow(dbError).named("value too long"),
			post("/preference/" + strings.Repeat("n", 130)).raw("x").allow(dbError).named("name too long"),
		},
	},
	{
		Name: "web push",
		Steps: []Step{
			register(""),
			// VAPID_PUBLIC is unset in compose.yml.
			get("/notification/web_push_keys"),
			get("/notification/web_push").named("no subscriptions"),
			post("/notification/web_push").form("endpoint", "https://push.example.com/sub/1", "auth", "a1", "key", "k1"),
			post("/notification/web_push").form("endpoint", "https://push.example.com/sub/1", "auth", "a2", "key", "k2").
				named("update subscription"),
			post("/notification/web_push").form("endpoint", "https://push.example.com/sub/2", "auth", "b", "key", "c"),
			get("/notification/web_push"),
			req("DELETE", "/notification/web_push/https://push.example.com/sub/1"),
			req("DELETE", "/notification/web_push/").named("delete with empty endpoint"),
			get("/notification/web_push").named("subscriptions after delete"),
			post("/notification/web_push").form("auth", "a").named("subscribe without endpoint"),
		},
	},
	{
		Name: "apn",
		Steps: []Step{
			register(""),
			get("/notification/apn/config"),
			post("/notification/apn/device").raw("device-1"),
			post("/notification/apn/device").raw("device-1").named("device again"),
			post("/notification/apn/device").raw(strings.Repeat("d", 300)).allow(dbError).named("device too long"),
			post("/v1/devices/device-2/registrations/web.party.relaychat").as("anon").
				header("Authorization", "ApplePushNotifications {{user}}"),
			post("/v1/devices/device-3/registrations/web.party.relaychat").as("anon").
				named("register without authorization"),
			post("/v1/devices/device-3/registrations/web.party.relaychat").as("anon").
				header("Authorization", "Basic {{user}}").named("register with the wrong scheme"),
			query(`SELECT device_id FROM apn ORDER BY device_id`),
			req("DELETE", "/v1/devices/device-2/registrations/web.party.relaychat").as("anon").
				header("Authorization", "ApplePushNotifications {{user}}"),
			req("DELETE", "/v1/devices/device-1/registrations/web.party.relaychat").as("anon").
				named("unregister without authorization"),
			query(`SELECT device_id FROM apn ORDER BY device_id`).named("devices after unregister"),
			post("/v1/log").as("anon").raw(`{"logs":["hello"]}`),
			post("/v1/pushPackages/web.party.relaychat").as("anon").raw("not json").
				allow(libraryError("JSON parse error text")),
			// No signing certificate in either container: both fail, with
			// their own error text.
			post("/v1/pushPackages/web.party.relaychat").as("anon").raw(`{"user":"{{user}}"}`).
				allow(libraryError("openssl/signing failure text")).
				named("push package without certificates"),
		},
	},
	{
		Name: "images",
		Steps: []Step{
			register(""),
			get("/image").named("no images"),
			seed(
				`INSERT INTO image ("user", url, delete_hash, created) VALUES
				('{{user}}', 'https://i.imgur.com/a.png', 'ha', NOW() - interval '2 hours'),
				('{{user}}', 'https://i.imgur.com/b.png', 'hb', NOW() - interval '1 hour'),
				('{{user}}', 'https://i.imgur.com/c.png', 'hc', NOW())`,
			),
			get("/image").allow(sameFormat("$[*].created", timestampFormat)),
			get("/image?limit=1&page=1").allow(sameFormat("$[*].created", timestampFormat)),
			// min("abc" || 15, 50) is the string "abc", which Postgres rejects
			// as a LIMIT (400, error null); page "abc" numifies to 0 in
			// $page * $limit.
			get("/image?limit=abc").allow(sameFormat("$[*].created", timestampFormat)),
			get("/image?page=abc").allow(sameFormat("$[*].created", timestampFormat)),
			// Perl's Imgur URL is hard-coded: only paths that never reach
			// Imgur are compared.
			post("/image").form("a", "b").named("upload without a file"),
			req("DELETE", "/image/https://i.imgur.com/missing.png"),
			req("DELETE", "/image/").named("delete with empty url"),
		},
	},
}

// seedLog inserts log rows 1-8 (ids are deterministic: the database is
// truncated with RESTART IDENTITY before each run).
var seedLog = []string{
	logRow(1, "{{conn}}", "{{user}}", "#a", "PRIVMSG", false, false, "NOW()", "bob", "hello world"),
	logRow(2, "{{conn}}", "{{user}}", "#a", "PRIVMSG", false, true, "NOW()", "alice", "hello again"),
	logRow(3, "{{conn}}", "{{user}}", "#a", "PRIVMSG", true, false, "NOW()", "bob", "alice: ping"),
	logRow(4, "{{conn}}", "{{user}}", "#a", "JOIN", false, false, "NOW()", "carol", ""),
	logRow(5, "{{conn}}", "{{user}}", "#b", "PRIVMSG", true, false, "NOW()", "dave", "hello from b"),
	logRow(6, "{{conn}}", "{{user}}", "#a", "PRIVMSG", false, false, "'2020-01-02 10:00:00'", "bob", "an old message"),
	logRow(7, "{{conn}}", "{{user}}", "#a", "PRIVMSG", false, false, "'2020-01-02 11:00:00'", "eve", "old search term"),
	logRow(8, "{{bob_conn}}", "{{bob_id}}", "#a", "PRIVMSG", true, false, "NOW()", "x", "bob's highlight"),
}

func logRow(n int, conn, user, channel, command string, highlight, self bool, at, from, text string) string {
	params := []string{channel}
	if text != "" {
		params = append(params, text)
	}
	msg, _ := json.Marshal(map[string]any{
		"Command": command,
		"Prefix":  map[string]string{"Name": from, "User": "~" + from, "Server": "example.com"},
		"Params":  params,
		"Time":    1761200000.25 + float64(n),
		"Raw":     fmt.Sprintf(":%s %s %s", from, command, strings.Join(params, " ")),
		"Tags":    map[string]string{"msgid": fmt.Sprintf("m%d", n)},
	})
	return fmt.Sprintf(`INSERT INTO log (connection, "user", channel, command, highlight, self, time, message)
		VALUES ('%s', '%s', '%s', '%s', %t, %t, %s, '%s')`,
		conn, user, channel, command, highlight, self, at, strings.ReplaceAll(string(msg), "'", "''"))
}

func logSteps() []Step {
	ch := "/connection/{{conn}}/channel"
	return []Step{
		// message.log, message.missed, message.seen, message.highlight:
		// MessageId, Highlight and Self value types and the /missed counts.
		get("/log/0"),
		get("/log/5"),
		get("/log/abc").allow(dbError),
		get("/missed?{{conn}}-%23a=1"),
		get("/missed?{{conn}}-%23a=0&{{conn}}-%23b=0&{{conn}}-%23none=0"),
		get("/missed").named("missed without parameters"),
		get("/missed?{{bob_conn}}-%23a=0").named("missed on someone else's connection"),
		get("/missed?{{conn}}-%23a=abc").allow(dbError),
		get("/highlight"),
		get("/highlight/5"),
		get("/highlight?limit=1"),
		get("/highlight/abc").allow(dbError),
		get("/seen").named("nothing seen"),
		post(ch + "/%23A/seen").raw("3"),
		post(ch + "/%23a/seen").raw("4").named("update seen"),
		get("/seen"),
		post(ch + "/%23a/seen").raw("abc").allow(dbError).named("seen not a number"),

		// channel.*
		get(ch),
		get(ch + "/%23A/events"),
		get(ch + "/%23a/events?limit=2"),
		get(ch + "/%23a/events/3"),
		get(ch + "/%23a/events/abc").allow(dbError),
		get(ch + "/%23a/last?query=hello"),
		get(ch + "/%23a/last?query=hello&limit=1"),
		get(ch + "/%23a/date/2020-01-01/2020-01-03"),
		get(ch + "/%23a/date/2020-01-01/2020-01-03?text=search&sender=eve"),
		get(ch + "/%23a/date/2020-01-05/2020-01-06").named("date range without rows"),
		get("/connection/{{bob_conn}}/channel/%23a/events").allow(invalidConn),

		// ignore.*
		get("/ignore").named("no ignores"),
		post(ch+"/%23A/ignore").form("from", "bob"),
		post(ch+"/%23a/ignore").form("from", "bob").named("duplicate ignore"),
		post(ch + "/%23a/ignore").named("ignore without from"),
		get("/ignore"),
		req("DELETE", ch+"/%23a/ignore/whatever?from=bob"),
		get("/ignore").named("ignores after delete"),
		req("DELETE", "/connection/{{bob_conn}}/channel/%23a/ignore/x?from=bob").allow(invalidConn),
	}
}
