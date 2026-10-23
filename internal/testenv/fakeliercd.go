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
	f := &LiercdServer{
		statuses:  map[string]string{},
		created:   map[string]bool{},
		overrides: map[string]override{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	f.URL = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

// SetStatus sets the status JSON returned for id once it exists.
func (f *LiercdServer) SetStatus(id, statusJSON string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[id] = statusJSON
}

// Respond forces a response for method and path (e.g. "GET", "/abc/status").
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

func (f *LiercdServer) handle(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, LiercdCall{r.Method, r.URL.Path, string(b)})

	if o, ok := f.overrides[r.Method+" "+r.URL.Path]; ok {
		w.WriteHeader(o.code)
		io.WriteString(w, o.body)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
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
