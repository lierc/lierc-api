package testenv

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type LiercdCall struct{ Method, Path, Body string }

// LiercdServer stands in for liercd. By default `/:id/status` is 404 until
// `/:id/create` has been called, then it returns the status set for the id.
type LiercdServer struct {
	URL string

	mu        sync.Mutex
	statuses  map[string]string
	created   map[string]bool
	overrides map[string]override
	calls     []LiercdCall
	srv       *httptest.Server
}

type override struct {
	code int
	body string
}

// FakeLiercd starts a LiercdServer, closed when t finishes.
func FakeLiercd(t testing.TB) *LiercdServer {
	f := NewLiercd()
	f.srv = httptest.NewServer(f)
	f.URL = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

// NewLiercd returns a LiercdServer that is not listening; serve it with any
// http.Server (the standalone fakes binary does). URL is left empty.
func NewLiercd() *LiercdServer {
	f := &LiercdServer{}
	f.Reset()
	return f
}

// Reset forgets every status, created id, forced response and call.
func (f *LiercdServer) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = map[string]string{}
	f.created = map[string]bool{}
	f.overrides = map[string]override{}
	f.calls = nil
}

// SetStatus sets the status JSON returned for id once it exists.
func (f *LiercdServer) SetStatus(id, statusJSON string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[id] = statusJSON
}

// Respond forces a response for method and path (e.g. "GET", "/abc/status").
// A path of the form "/*/<action>" matches that action for every id, after
// any exact path.
func (f *LiercdServer) Respond(method, path string, code int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.overrides[method+" "+path] = override{code, body}
}

func (f *LiercdServer) Calls() []LiercdCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]LiercdCall(nil), f.calls...)
}

// ServeHTTP answers one liercd request.
func (f *LiercdServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, LiercdCall{r.Method, r.URL.Path, string(b)})

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	keys := []string{r.Method + " " + r.URL.Path}
	if len(parts) == 2 {
		keys = append(keys, r.Method+" /*/"+parts[1])
	}
	for _, k := range keys {
		if o, ok := f.overrides[k]; ok {
			w.WriteHeader(o.code)
			io.WriteString(w, o.body)
			return
		}
	}
	if len(parts) == 2 {
		id := parts[0]
		switch {
		case parts[1] == "create" && r.Method == http.MethodPost:
			f.created[id] = true
			io.WriteString(w, "{}")
			return
		case parts[1] == "status" && r.Method == http.MethodGet && f.created[id]:
			s, ok := f.statuses[id]
			if !ok {
				s = "{}"
			}
			io.WriteString(w, s)
			return
		}
	}
	http.NotFound(w, r)
}
