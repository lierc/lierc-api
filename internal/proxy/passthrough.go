package proxy

import (
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// unavailable is the Perl API's error shape, so the login and register
// pages can show it.
const unavailable = `{"status":"error","error":"API unavailable"}`

// PassThrough passes requests to the Perl API at target and its responses
// back, unchanged. Host is kept (Perl's same-origin check compares it with
// Origin), and X-Forwarded-For is replaced by the client address: Wrap has
// already settled that from a trusted proxy, and Perl takes the last hop,
// so nothing a client sent reaches Perl. Responses are flushed as they're
// copied, for the date search's event stream.
func PassThrough(target *url.URL) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = r.In.Host
			host, _, err := net.SplitHostPort(r.In.RemoteAddr)
			if err != nil {
				host = r.In.RemoteAddr
			}
			r.Out.Header.Set("X-Forwarded-For", host)
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("perl api: %s %s: %v", r.Method, r.URL.Path, err)
			w.Header().Set("Content-Type", "application/javascript;charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(unavailable))
		},
	}
}
