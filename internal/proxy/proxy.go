// Package proxy applies X-Forwarded-For from trusted proxies and writes the
// access log, as bin/api.psgi does with Plack::Middleware::ReverseProxy
// (enabled for 127.0.0.1) and Plack::Middleware::AccessLog.
package proxy

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"
)

var logOutput io.Writer = os.Stderr

var lastHop = regexp.MustCompile(`([^,\s]+)$`)

// Wrap rewrites RemoteAddr to the last X-Forwarded-For hop when the peer is in
// trusted (IPs or CIDRs), then logs the request in combined log format.
func Wrap(trusted []string, next http.Handler) http.Handler {
	var ips []net.IP
	var nets []*net.IPNet
	for _, t := range trusted {
		if _, n, err := net.ParseCIDR(t); err == nil {
			nets = append(nets, n)
		} else if ip := net.ParseIP(t); ip != nil {
			ips = append(ips, ip)
		}
	}
	isTrusted := func(ip net.IP) bool {
		if ip == nil {
			return false
		}
		for _, t := range ips {
			if t.Equal(ip) {
				return true
			}
		}
		for _, n := range nets {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host, port = r.RemoteAddr, ""
		}
		if isTrusted(net.ParseIP(host)) {
			// Plack::Middleware::ReverseProxy: REMOTE_ADDR = last XFF entry.
			if m := lastHop.FindStringSubmatch(r.Header.Get("X-Forwarded-For")); m != nil {
				host = m[1]
				r.RemoteAddr = net.JoinHostPort(host, port)
			}
		}

		start := time.Now()
		lw := &logWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(lw, r)

		uri := r.RequestURI
		if uri == "" {
			uri = r.URL.RequestURI()
		}
		size := "-"
		if lw.bytes > 0 {
			size = strconv.FormatInt(lw.bytes, 10)
		}
		fmt.Fprintf(logOutput, "%s - - [%s] \"%s %s %s\" %d %s \"%s\" \"%s\"\n",
			host, start.Format("02/Jan/2006:15:04:05 -0700"),
			r.Method, uri, r.Proto, lw.status, size,
			orDash(r.Referer()), orDash(r.UserAgent()))
	})
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type logWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	bytes       int64
}

func (w *logWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *logWriter) WriteHeader(code int) {
	if !w.wroteHeader && code >= 200 {
		w.status, w.wroteHeader = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *logWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *logWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
