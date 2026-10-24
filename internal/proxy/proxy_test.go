package proxy

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func serve(t *testing.T, trusted []string, peer, xff string) (remote string, logLine string) {
	t.Helper()
	var buf bytes.Buffer
	old := logOutput
	logOutput = &buf
	t.Cleanup(func() { logOutput = old })

	h := Wrap(trusted, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote = r.RemoteAddr
		w.WriteHeader(401)
		io.WriteString(w, "nope")
	}))
	req := httptest.NewRequest("GET", "/health?x=1", nil)
	req.RemoteAddr = peer
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	req.Header.Set("User-Agent", "ua/1")
	h.ServeHTTP(httptest.NewRecorder(), req)
	return remote, buf.String()
}

func TestTrustedPeerUsesForwardedFor(t *testing.T) {
	remote, line := serve(t, []string{"127.0.0.1"}, "127.0.0.1:5555", "1.1.1.1, 9.9.9.9")
	if remote != "9.9.9.9:5555" {
		t.Fatalf("RemoteAddr = %q", remote)
	}
	re := regexp.MustCompile(`^9\.9\.9\.9 - - \[[^\]]+\] "GET /health\?x=1 HTTP/1\.1" 401 4 "-" "ua/1"\n$`)
	if !re.MatchString(line) {
		t.Fatalf("log line = %q", line)
	}
}

func TestTrustedCIDR(t *testing.T) {
	remote, _ := serve(t, []string{"10.0.0.0/8"}, "10.1.2.3:1", "8.8.8.8")
	if remote != "8.8.8.8:1" {
		t.Fatalf("RemoteAddr = %q", remote)
	}
}

func TestUntrustedPeerIgnoresForwardedFor(t *testing.T) {
	remote, line := serve(t, []string{"127.0.0.1"}, "5.5.5.5:5555", "9.9.9.9")
	if remote != "5.5.5.5:5555" {
		t.Fatalf("RemoteAddr = %q", remote)
	}
	if !regexp.MustCompile(`^5\.5\.5\.5 `).MatchString(line) {
		t.Fatalf("log line = %q", line)
	}
}

func TestTrustedPeerWithoutHeader(t *testing.T) {
	remote, _ := serve(t, []string{"127.0.0.1"}, "127.0.0.1:5555", "")
	if remote != "127.0.0.1:5555" {
		t.Fatalf("RemoteAddr = %q", remote)
	}
}

func TestTrustedIPv6Loopback(t *testing.T) {
	trusted := []string{"127.0.0.1", "::1"}
	remote, line := serve(t, trusted, "[::1]:5555", "9.9.9.9")
	if remote != "9.9.9.9:5555" {
		t.Fatalf("RemoteAddr = %q", remote)
	}
	if !regexp.MustCompile(`^9\.9\.9\.9 `).MatchString(line) {
		t.Fatalf("log line = %q", line)
	}
	if remote, _ := serve(t, trusted, "[::2]:5555", "9.9.9.9"); remote != "[::2]:5555" {
		t.Fatalf("untrusted v6 RemoteAddr = %q", remote)
	}
}

type failFlusher struct{ *httptest.ResponseRecorder }

var errFlush = errors.New("flush failed")

func (failFlusher) FlushError() error { return errFlush }

func TestFlushErrorPropagates(t *testing.T) {
	old := logOutput
	logOutput = io.Discard
	t.Cleanup(func() { logOutput = old })
	var got error
	h := Wrap(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = http.NewResponseController(w).Flush()
	}))
	h.ServeHTTP(failFlusher{httptest.NewRecorder()}, httptest.NewRequest("GET", "/", nil))
	if !errors.Is(got, errFlush) {
		t.Fatalf("Flush() = %v", got)
	}
}
