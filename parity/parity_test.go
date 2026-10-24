//go:build parity

package parity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/http"
	"net/http/cookiejar"
	"net/mail"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Endpoints of the compose stack (parity/compose.yml), overridable by env.
var (
	perlAPI    = envOr("PARITY_PERL_API", "http://127.0.0.1:5004")
	perlStream = envOr("PARITY_PERL_STREAM", "http://127.0.0.1:5003")
	goAPI      = envOr("PARITY_GO_API", "http://127.0.0.1:5014")
	goStream   = envOr("PARITY_GO_STREAM", "http://127.0.0.1:5013")
	dbURL      = envOr("PARITY_DB", "postgres://lierc:lierc@127.0.0.1:15432/lierc?sslmode=disable")
	nsqdURL    = envOr("PARITY_NSQD", "http://127.0.0.1:14151")
	fakesURL   = envOr("PARITY_FAKES", "http://127.0.0.1:18090")
	apiKey     = envOr("PARITY_API_KEY", "parity-key")
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	if err := waitReady(3 * time.Minute); err != nil {
		fmt.Fprintln(os.Stderr, "parity: stack not ready:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// waitReady waits until every server answers HTTP and lierc.sql is loaded.
func waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if last = ready(); last == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return last
}

func ready() error {
	c := http.Client{Timeout: 5 * time.Second}
	for _, u := range []string{perlAPI + "/health", goAPI + "/health", perlStream + "/stats",
		goStream + "/stats", fakesURL + "/health", nsqdURL + "/ping"} {
		res, err := c.Get(u)
		if err != nil {
			return err
		}
		res.Body.Close()
	}
	ctx := context.Background()
	if pool == nil {
		p, err := pgxpool.New(ctx, dbURL)
		if err != nil {
			return err
		}
		pool = p
	}
	_, err := pool.Exec(ctx, `SELECT 1 FROM "user" LIMIT 1`)
	return err
}

// reset empties every table (restarting the log id sequence, so seeded
// MessageIds match between runs) and forgets all fake state.
func reset(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, `SELECT quote_ident(tablename) FROM pg_tables WHERE schemaname='public'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE "+strings.Join(tables, ", ")+" RESTART IDENTITY CASCADE"); err != nil {
		t.Fatal(err)
	}
	fakes(t, "POST", "/reset", nil)
}

// fakes calls the fakes control API and returns the response body.
func fakes(t *testing.T, method, path string, body any) []byte {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, fakesURL+path, r)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fakes %s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode/100 != 2 {
		t.Fatalf("fakes %s %s: %d %s", method, path, res.StatusCode, b)
	}
	return b
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// run is one scenario's run against one server.
type run struct {
	t    *testing.T
	base string
	jars map[string]http.CookieJar
	vars map[string]string
}

func (r *run) client(as string) *http.Client {
	c := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: noRedirect}
	if as == "anon" {
		return c
	}
	if as == "" {
		as = "alice"
	}
	if r.jars[as] == nil {
		r.jars[as], _ = cookiejar.New(nil)
	}
	c.Jar = r.jars[as]
	return c
}

func (r *run) expand(s string) string { return Expand(s, r.vars) }

func (r *run) step(st Step) Result {
	t, ctx := r.t, context.Background()
	for _, q := range st.SQL {
		if _, err := pool.Exec(ctx, r.expand(q)); err != nil {
			t.Fatalf("%s: seed: %v", st.Label(), err)
		}
	}
	for name, q := range st.CaptureSQL {
		var v string
		if err := pool.QueryRow(ctx, r.expand(q)).Scan(&v); err != nil {
			t.Fatalf("%s: capture %s: %v", st.Label(), name, err)
		}
		r.vars[name] = v
	}
	for _, l := range st.Liercd {
		fakes(t, "POST", "/liercd/respond", map[string]any{
			"method": l.Method, "path": r.expand(l.Path), "code": l.Code, "body": r.expand(l.Body)})
	}

	var res Result
	switch {
	case st.Query != "":
		res = r.query(r.expand(st.Query))
	case st.Mail:
		res = Result{Status: 200, Body: mailSummary(t, fakes(t, "GET", "/smtp/messages", nil))}
	case st.LiercdCalls:
		res = Result{Status: 200, Body: fakes(t, "GET", "/liercd/calls", nil)}
	case st.Method != "":
		res = r.request(st)
	default:
		return Result{}
	}

	if len(st.Capture) > 0 {
		v, err := decodeJSON(res.Body)
		for name, path := range st.Capture {
			got, ok := Lookup(v, path)
			if err != nil || !ok {
				t.Errorf("%s: %s: cannot capture %s at %s from %d %s", r.base, st.Label(), name, path, res.Status, clip(res.Body))
				continue
			}
			r.vars[name] = CaptureString(got)
		}
	}
	return res
}

func (r *run) request(st Step) Result {
	req, err := http.NewRequest(st.Method, r.base+r.expand(st.Path), strings.NewReader(r.expand(st.Body)))
	if err != nil {
		r.t.Fatalf("%s: %v", st.Label(), err)
	}
	for k, v := range st.Header {
		req.Header.Set(k, r.expand(v))
	}
	res, err := r.client(st.As).Do(req)
	if err != nil {
		return Result{Status: -1, Body: []byte(err.Error())}
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		b = append(b, []byte("\nread error: "+err.Error())...)
	}
	return Result{Status: res.StatusCode, ContentType: res.Header.Get("Content-Type"), Body: b}
}

func (r *run) query(q string) Result {
	rows, err := pool.Query(context.Background(), q)
	if err != nil {
		r.t.Fatalf("query %s: %v", q, err)
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			r.t.Fatal(err)
		}
		m := map[string]any{}
		for i, fd := range rows.FieldDescriptions() {
			m[fd.Name] = vals[i]
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		r.t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	return Result{Status: 200, Body: b}
}

// mailSummary reduces the fake SMTP server's messages to what a recipient
// sees: envelope, subject, content type and decoded body. Transport headers
// (Date, Message-ID, X-Mailer, ...) are left out.
func mailSummary(t *testing.T, raw []byte) []byte {
	var msgs []struct {
		From string
		To   []string
		Data string
	}
	if err := json.Unmarshal(raw, &msgs); err != nil {
		t.Fatal(err)
	}
	out := []map[string]any{}
	for _, m := range msgs {
		s := map[string]any{"envelope_from": m.From, "envelope_to": m.To}
		msg, err := mail.ReadMessage(strings.NewReader(m.Data))
		if err != nil {
			s["error"] = err.Error()
			out = append(out, s)
			continue
		}
		dec := new(mime.WordDecoder)
		for _, h := range []string{"From", "To", "Subject"} {
			v, err := dec.DecodeHeader(msg.Header.Get(h))
			if err != nil {
				v = msg.Header.Get(h)
			}
			s[h] = v
		}
		ct, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
		s["Content-Type"], s["charset"] = ct, strings.ToLower(params["charset"])
		var body io.Reader = msg.Body
		if strings.EqualFold(msg.Header.Get("Content-Transfer-Encoding"), "quoted-printable") {
			body = quotedprintable.NewReader(body)
		}
		b, _ := io.ReadAll(body)
		s["Body"] = strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
		out = append(out, s)
	}
	b, _ := json.Marshal(out)
	return b
}

func TestScenarios(t *testing.T) {
	for _, sc := range Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			runs := map[string]*run{}
			results := map[string][]Result{}
			for _, srv := range []struct{ name, base string }{{"perl", perlAPI}, {"go", goAPI}} {
				reset(t)
				r := &run{t: t, base: srv.base, jars: map[string]http.CookieJar{}, vars: map[string]string{}}
				for _, st := range sc.Steps {
					results[srv.name] = append(results[srv.name], r.step(st))
				}
				runs[srv.name] = r
			}
			for i, st := range sc.Steps {
				if st.Method == "" && st.Query == "" && !st.Mail && !st.LiercdCalls {
					continue
				}
				p, g := results["perl"][i], results["go"][i]
				p.Body = Normalize(p.Body, runs["perl"].vars)
				g.Body = Normalize(g.Body, runs["go"].vars)
				if diffs := Compare(p, g, st.Allow); len(diffs) > 0 {
					t.Errorf("step %d %s:\n  %s\n  perl: %d %s\n  go:   %d %s", i, st.Label(),
						strings.Join(diffs, "\n  "), p.Status, clip(p.Body), g.Status, clip(g.Body))
				}
			}
		})
	}
}

// Cross-server tests: sessions made by Perl, used on Go.

// perlRegister signs up a user on Perl and returns its chats cookie and the
// response body.
func perlRegister(t *testing.T, name string) (*http.Cookie, []byte) {
	t.Helper()
	form := url.Values{"username": {name}, "email": {name + "@example.com"}, "pass": {"hunter2"}}
	res, err := http.PostForm(perlAPI+"/register", form)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("perl register: %d %s", res.StatusCode, b)
	}
	c := cookieNamed(res, "chats")
	if c == nil {
		t.Fatalf("perl register set no chats cookie: %v", res.Header["Set-Cookie"])
	}
	return c, b
}

func cookieNamed(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// call makes a request with exactly the given cookies.
func call(t *testing.T, method, u string, cookies ...*http.Cookie) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, u, nil)
	for _, c := range cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	res, err := (&http.Client{CheckRedirect: noRedirect, Timeout: time.Minute}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func jsonEqual(t *testing.T, what string, got, want []byte) {
	t.Helper()
	if diffs := Compare(jsonResult(200, string(want)), jsonResult(200, string(got)), nil); len(diffs) > 0 {
		t.Errorf("%s: %v\n  got  %s\n  want %s", what, diffs, got, want)
	}
}

// migrate calls Go's /auth with a Perl chats cookie and returns the chats2
// cookie Go issues.
func migrate(t *testing.T, chats *http.Cookie) *http.Cookie {
	t.Helper()
	res, b := call(t, "GET", goAPI+"/auth", chats)
	if res.StatusCode != 200 {
		t.Fatalf("go /auth with chats: %d %s", res.StatusCode, b)
	}
	c := cookieNamed(res, "chats2")
	if c == nil || c.Value == "" {
		t.Fatalf("go /auth with chats issued no chats2: %v", res.Header["Set-Cookie"])
	}
	return c
}

func TestMigrationFromPerl(t *testing.T) {
	reset(t)
	chats, _ := perlRegister(t, "alice")
	_, perlAuth := call(t, "GET", perlAPI+"/auth", chats)

	res, goAuth := call(t, "GET", goAPI+"/auth", chats)
	if res.StatusCode != 200 {
		t.Fatalf("go /auth with chats: %d %s", res.StatusCode, goAuth)
	}
	jsonEqual(t, "go /auth with chats", goAuth, perlAuth)
	chats2 := cookieNamed(res, "chats2")
	if chats2 == nil || chats2.Value == "" {
		t.Fatalf("no chats2 cookie: %v", res.Header["Set-Cookie"])
	}

	res, again := call(t, "GET", goAPI+"/auth", chats2)
	if res.StatusCode != 200 {
		t.Fatalf("go /auth with only chats2: %d %s", res.StatusCode, again)
	}
	jsonEqual(t, "go /auth with only chats2", again, perlAuth)
}

func TestPerlLogoutCookie(t *testing.T) {
	reset(t)
	chats, _ := perlRegister(t, "alice")
	res, b := call(t, "GET", perlAPI+"/logout", chats)
	if res.StatusCode != 200 {
		t.Fatalf("perl /logout: %d %s", res.StatusCode, b)
	}
	empty := cookieNamed(res, "chats") // the session cookie for {}
	if empty == nil {
		t.Fatalf("perl /logout set no chats cookie: %v", res.Header["Set-Cookie"])
	}
	if res, b := call(t, "GET", goAPI+"/auth", empty); res.StatusCode != 401 {
		t.Fatalf("go /auth with Perl's logged-out chats: %d %s", res.StatusCode, b)
	}
}

func TestGoLogoutStaysLoggedOut(t *testing.T) {
	reset(t)
	chats, _ := perlRegister(t, "alice")
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(goAPI)
	jar.SetCookies(u, []*http.Cookie{{Name: "chats", Value: chats.Value, Path: "/"}})
	c := &http.Client{Jar: jar, CheckRedirect: noRedirect, Timeout: time.Minute}
	get := func(path string) (int, string) {
		res, err := c.Get(goAPI + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, b := get("/auth"); code != 200 {
		t.Fatalf("migrate: %d %s", code, b)
	}
	if code, b := get("/logout"); code != 200 {
		t.Fatalf("logout: %d %s", code, b)
	}
	if code, b := get("/auth"); code != 401 {
		t.Fatalf("/auth after Go logout: %d %s (jar: %v)", code, b, jar.Cookies(u))
	}
}

// Stream tests.

type sseStream struct {
	status      int
	contentType string
	padding     string
	events      chan Event
	cancel      context.CancelFunc
}

func openStream(t *testing.T, base string, cookie *http.Cookie) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/", nil)
	req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open stream %s: %v", base, err)
	}
	s := &sseStream{status: res.StatusCode, contentType: res.Header.Get("Content-Type"),
		events: make(chan Event, 1000), cancel: cancel}
	t.Cleanup(func() { cancel(); res.Body.Close() })
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("stream %s: %d %s", base, res.StatusCode, b)
	}
	br := bufio.NewReader(res.Body)
	first, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("stream %s: reading padding: %v", base, err)
	}
	s.padding = first
	go func() {
		defer close(s.events)
		var cur Event
		open := false
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSuffix(line, "\n")
			switch {
			case line == "":
				if open {
					s.events <- cur
				}
				cur, open = Event{}, false
			case strings.HasPrefix(line, ":"):
				s.events <- Event{Comment: line}
			default:
				k, v, _ := strings.Cut(line, ":")
				v = strings.TrimPrefix(v, " ")
				open = true
				switch k {
				case "event":
					cur.Event = v
				case "data":
					cur.Data = v
				case "id":
					cur.ID = v
				}
			}
		}
	}()
	return s
}

// collect returns events (pings excluded) until none arrives for quiet. It
// reports whether the stream is still open.
func (s *sseStream) collect(quiet time.Duration) ([]Event, bool) {
	var out []Event
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				return out, false
			}
			if ev.Event != "ping" {
				out = append(out, ev)
			}
		case <-time.After(quiet):
			return out, true
		}
	}
}

// firstPing reads the ping that follows the padding.
func (s *sseStream) firstPing(t *testing.T, name string) Event {
	t.Helper()
	select {
	case ev, ok := <-s.events:
		if !ok || ev.Event != "ping" {
			t.Fatalf("%s: first event %+v (open %v), want ping", name, ev, ok)
		}
		return ev
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: no ping", name)
	}
	return Event{}
}

// replayStep is the push_fake_events step (Events.pm) an event belongs to:
// connect, welcome, joins, topics, nicks, modes.
var replayStep = map[string]int{
	"CREATE": 0, "CONNECT": 0, "001": 1, "005": 1, "CAP": 1, "JOIN": 2,
	"332": 3, "333": 3, "353": 4, "366": 4, "324": 5,
}

// replaySteps groups replay events by step, each step's events as sorted
// canonical JSON with Message.Time removed (its type is kept). It fails if
// an event is out of step order.
func replaySteps(t *testing.T, name string, evs []Event) [6][]string {
	t.Helper()
	var steps [6][]string
	last := 0
	for _, ev := range evs {
		v, err := decodeJSON([]byte(ev.Data))
		m, _ := v.(map[string]any)
		msg, _ := m["Message"].(map[string]any)
		if err != nil || ev.Event != "irc" || msg == nil {
			t.Errorf("%s: unexpected replay event %+v", name, ev)
			continue
		}
		cmd, _ := msg["Command"].(string)
		step, ok := replayStep[cmd]
		if !ok {
			t.Errorf("%s: unexpected replay command %q", name, cmd)
			continue
		}
		if step < last {
			t.Errorf("%s: %s arrived after step %d", name, cmd, last)
		}
		last = step
		msg["Time"] = typeName(msg["Time"])
		if ev.ID != "" {
			m["(id)"] = ev.ID
		}
		b, _ := json.Marshal(m)
		steps[step] = append(steps[step], string(b))
	}
	for i := range steps {
		sort.Strings(steps[i])
	}
	return steps
}

func compareReplay(t *testing.T, perl, goEvs []Event) {
	t.Helper()
	ps, gs := replaySteps(t, "perl", perl), replaySteps(t, "go", goEvs)
	for i := range ps {
		if strings.Join(ps[i], "\n") != strings.Join(gs[i], "\n") {
			t.Errorf("replay step %d differs:\n  perl: %v\n  go:   %v", i, ps[i], gs[i])
		}
	}
}

func publish(t *testing.T, body string) {
	t.Helper()
	res, err := http.Post(nsqdURL+"/pub?topic=logged", "application/octet-stream", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("nsqd pub: %d %s", res.StatusCode, b)
	}
}

// awaitLive publishes probes for conn until every stream has delivered one,
// so both servers' NSQ consumers are subscribed. Other events are dropped.
func awaitLive(t *testing.T, conn string, streams map[string]*sseStream) {
	t.Helper()
	probe := fmt.Sprintf(`{"ConnectionId":%q,"MessageId":0,"probe":true}`, conn)
	deadline := time.Now().Add(time.Minute)
	for name, s := range streams {
		for seen := false; !seen; {
			if time.Now().After(deadline) {
				t.Fatalf("%s stream never delivered a probe", name)
			}
			publish(t, probe)
			evs, open := s.collect(300 * time.Millisecond)
			for _, ev := range evs {
				seen = seen || strings.Contains(ev.Data, `"probe":true`)
			}
			if !open {
				t.Fatalf("%s stream closed", name)
			}
		}
	}
	for _, s := range streams {
		s.collect(500 * time.Millisecond) // drain late probes
	}
}

func insertConn(t *testing.T, id, user, config string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO connection (id, "user", config) VALUES ($1, $2, $3)`, id, user, config); err != nil {
		t.Fatal(err)
	}
}

// streamUser registers alice on Perl and migrates her to Go, returning both
// cookies and her id.
func streamUser(t *testing.T) (chats, chats2 *http.Cookie, user string) {
	t.Helper()
	chats, body := perlRegister(t, "alice")
	var show struct{ ID string }
	if err := json.Unmarshal(body, &show); err != nil || show.ID == "" {
		t.Fatalf("register body %s", body)
	}
	return chats, migrate(t, chats), show.ID
}

const statusA = `{"Id":"connA","Nick":"alice","Registered":true,
	"Config":{"Host":"irc.a.net","Alias":"A"},"Status":{"Connected":true},
	"Isupport":["CHANTYPES=#","NETWORK=A"],"CapsAcked":["server-time","echo-message"],
	"Channels":[
	  {"Name":"#one","Mode":"+nt","Topic":{"Topic":"hello","User":"bob","Time":1700000000},"Nicks":["alice","bob","@carol"]},
	  {"Name":"#two","Topic":{"Topic":""},"Nicks":["alice"]}
	]}`

const statusB = `{"Id":"connB","Nick":"alice_","Registered":false,
	"Config":{"Host":"irc.b.net"},"Status":{"Connected":false},
	"Isupport":[],"Channels":[]}`

func TestStreamParity(t *testing.T) {
	reset(t)
	chats, chats2, user := streamUser(t)
	insertConn(t, "connA", user, `{"Host":"irc.a.net","Alias":"A","Nick":"alice"}`)
	insertConn(t, "connB", user, `{"Host":"irc.b.net","Nick":"alice_"}`)
	fakes(t, "POST", "/liercd/status", map[string]string{"id": "connA", "status": statusA})
	fakes(t, "POST", "/liercd/status", map[string]string{"id": "connB", "status": statusB})

	// Unauthenticated and /stats parity.
	for _, path := range []string{"/", "/stats"} {
		p := streamGet(t, perlStream+path, "")
		g := streamGet(t, goStream+path, "")
		if diffs := Compare(p, g, nil); len(diffs) > 0 {
			t.Errorf("stream GET %s: %v", path, diffs)
		}
	}

	streams := map[string]*sseStream{
		"perl": openStream(t, perlStream, chats),
		"go":   openStream(t, goStream, chats2),
	}
	p, g := streams["perl"], streams["go"]
	if p.contentType != g.contentType {
		t.Errorf("Content-Type: perl %q, go %q", p.contentType, g.contentType)
	}
	if p.padding != g.padding {
		t.Errorf("padding: perl %q, go %q", p.padding, g.padding)
	}
	pp, gp := p.firstPing(t, "perl"), g.firstPing(t, "go")
	if diffs := Compare(jsonResult(200, `"`+pp.Data+`"`), jsonResult(200, `"`+gp.Data+`"`),
		[]Allow{sameFormat("$", "Time::HiRes::time at the moment of the ping")}); len(diffs) > 0 {
		t.Errorf("ping data: %v", diffs)
	}

	pr, _ := p.collect(3 * time.Second)
	gr, _ := g.collect(3 * time.Second)
	if len(pr) == 0 {
		t.Error("perl sent no replay events")
	}
	compareReplay(t, pr, gr)

	for _, path := range []string{"/stats", "/stats?user=" + user} {
		pres := streamGet(t, perlStream+path, apiKey)
		gres := streamGet(t, goStream+path, apiKey)
		pres.Body, gres.Body = Normalize(pres.Body, map[string]string{"user": user}), Normalize(gres.Body, map[string]string{"user": user})
		if diffs := Compare(pres, gres, []Allow{
			anyValue("$[*].created", "stream start time"),
			anyValue("$[*].remote", "client address as seen through the docker port mapping"),
		}); len(diffs) > 0 {
			t.Errorf("stream GET %s with key: %v\n  perl %s\n  go   %s", path, diffs, pres.Body, gres.Body)
		}
	}

	awaitLive(t, "connA", streams)
	var sent []string
	for i := 1; i <= 20; i++ {
		body := fmt.Sprintf(`{"ConnectionId":"connA","MessageId":%d,"Message":{"Command":"PRIVMSG","Prefix":{"Name":"bob"},"Params":["#one","message %d"]}}`, i, i)
		sent = append(sent, body)
		publish(t, body)
		// Spaced out: Perl looks each message's owner up on an async
		// connection pool, so a burst could be delivered out of order.
		time.Sleep(25 * time.Millisecond)
	}
	// A message for a connection nobody owns is dropped by both.
	publish(t, `{"ConnectionId":"nobody","MessageId":99,"Message":{}}`)

	for name, s := range streams {
		var got []Event
		deadline := time.After(20 * time.Second)
	read:
		for len(got) < len(sent) {
			select {
			case ev, ok := <-s.events:
				if !ok {
					t.Fatalf("%s stream closed after %d messages", name, len(got))
				}
				if ev.Event == "ping" || strings.Contains(ev.Data, `"probe":true`) {
					continue
				}
				got = append(got, ev)
			case <-deadline:
				break read
			}
		}
		if len(got) != len(sent) {
			t.Errorf("%s stream delivered %d of %d messages: %+v", name, len(got), len(sent), got)
			continue
		}
		for i, ev := range got {
			if ev.Event != "irc" || ev.Data != sent[i] || ev.ID != fmt.Sprint(i+1) {
				t.Errorf("%s message %d: %+v, want irc %s id %d", name, i+1, ev, sent[i], i+1)
			}
		}
		if extra, _ := s.collect(time.Second); len(extra) > 0 {
			t.Errorf("%s stream delivered extra events: %+v", name, extra)
		}
	}
}

// One connection's liercd create fails. Perl's push_fake_events then never
// fires its condvar (the croak escapes the callback), so it may send no
// replay at all while the stream stays open; whatever Perl does, Go must
// match it.
func TestStreamReplayOneConnectionFails(t *testing.T) {
	reset(t)
	chats, chats2, user := streamUser(t)
	insertConn(t, "connA", user, `{"Host":"irc.a.net","Alias":"A","Nick":"alice"}`)
	insertConn(t, "connB", user, `{"Host":"irc.bad"}`)
	fakes(t, "POST", "/liercd/status", map[string]string{"id": "connA", "status": statusA})
	fakes(t, "POST", "/liercd/respond", map[string]any{"method": "GET", "path": "/connB/status", "code": 500, "body": "boom"})
	fakes(t, "POST", "/liercd/respond", map[string]any{"method": "POST", "path": "/connB/create", "code": 500, "body": "boom"})

	streams := map[string]*sseStream{
		"perl": openStream(t, perlStream, chats),
		"go":   openStream(t, goStream, chats2),
	}
	streams["perl"].firstPing(t, "perl")
	streams["go"].firstPing(t, "go")
	pr, popen := streams["perl"].collect(3 * time.Second)
	gr, gopen := streams["go"].collect(3 * time.Second)
	t.Logf("replay events: perl %d, go %d", len(pr), len(gr))
	if popen != gopen {
		t.Fatalf("stream open after replay: perl %v, go %v", popen, gopen)
	}
	compareReplay(t, pr, gr)
	if popen {
		// Both still open: both deliver live events.
		awaitLive(t, "connA", streams)
	}
}

// streamGet makes a plain request to a stream server, with the stats key
// when key is set.
func streamGet(t *testing.T, u, key string) Result {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	if key != "" {
		req.Header.Set("Lierc-Key", key)
	}
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return Result{Status: res.StatusCode, ContentType: res.Header.Get("Content-Type"), Body: b}
}
