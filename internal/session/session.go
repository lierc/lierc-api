// Package session implements the chats2 session cookie and the read-only
// fallback to Perl's Plack::Middleware::Session::Cookie "chats" cookie.
package session

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lierc/lierc-api/internal/storable"
)

const (
	cookieName     = "chats2"
	perlCookieName = "chats"
	maxAge         = 7 * 24 * time.Hour
)

// ReadPerl verifies and decodes a Perl "chats" cookie value
// (URL-escaped "<id>:<base64 Storable>:<hmac_sha1_hex(base64, secret)>").
func ReadPerl(cookieValue, secret string) (user string, ok bool) {
	v, err := url.PathUnescape(cookieValue) // uri_unescape: '+' stays '+'
	if err != nil {
		return "", false
	}
	parts := strings.Split(v, ":")
	if len(parts) != 3 {
		return "", false
	}
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(parts[1]))
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(parts[2])) {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	h, err := storable.DecodeHash(raw)
	if err != nil || h["user"] == "" {
		return "", false
	}
	return h["user"], true
}

// Codec signs and verifies chats2 values and provides the session middleware.
type Codec struct {
	Secret string
	Secure bool
	Now    func() time.Time
}

func (c Codec) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Codec) sign(user, expiry string) string {
	mac := hmac.New(sha256.New, []byte(c.Secret))
	mac.Write([]byte("chats2|" + user + "|" + expiry))
	return hex.EncodeToString(mac.Sum(nil))
}

// Encode returns a chats2 value expiring 7 days from now.
func (c Codec) Encode(user string) string {
	exp := strconv.FormatInt(c.now().Add(maxAge).Unix(), 10)
	return user + "." + exp + "." + c.sign(user, exp)
}

// Decode verifies a chats2 value. User ids must not contain '.'.
func (c Codec) Decode(value string) (user string, ok bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] == "" {
		return "", false
	}
	for _, r := range parts[1] {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || exp <= c.now().Unix() {
		return "", false
	}
	if !hmac.Equal([]byte(c.sign(parts[0], parts[1])), []byte(parts[2])) {
		return "", false
	}
	return parts[0], true
}

// Session is the per-request session state.
type Session struct {
	user    string
	cleared bool
}

func (s *Session) User() string { return s.user }

func (s *Session) SetUser(id string) { s.user, s.cleared = id, false }

// Clear logs the user out; the response expires both cookies.
func (s *Session) Clear() { s.user, s.cleared = "", true }

type ctxKey struct{}

// FromContext returns the request's session, or nil without the middleware.
func FromContext(ctx context.Context) *Session {
	s, _ := ctx.Value(ctxKey{}).(*Session)
	return s
}

// Middleware loads the session from cookies and writes cookies just before the
// first header write of the response.
func (c Codec) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := &Session{}
		for _, ck := range r.Cookies() {
			if ck.Name == cookieName {
				if u, ok := c.Decode(ck.Value); ok {
					s.user = u
					break
				}
			}
		}
		if s.user == "" {
			for _, ck := range r.Cookies() {
				if ck.Name == perlCookieName {
					if u, ok := ReadPerl(ck.Value, c.Secret); ok {
						s.user = u
						break
					}
				}
			}
		}
		sw := &cookieWriter{ResponseWriter: w, c: c, s: s}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), ctxKey{}, s)))
		// Handler wrote nothing: net/http will send an implicit 200 after we return.
		sw.setCookies()
	})
}

type cookieWriter struct {
	http.ResponseWriter
	c    Codec
	s    *Session
	sent bool
}

func (w *cookieWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *cookieWriter) setCookies() {
	if w.sent {
		return
	}
	w.sent = true
	switch {
	case w.s.user != "":
		// Sliding expiry: re-issue on every logged-in response.
		http.SetCookie(w.ResponseWriter, &http.Cookie{
			Name: cookieName, Value: w.c.Encode(w.s.user), Path: "/",
			MaxAge: int(maxAge / time.Second), Expires: w.c.now().Add(maxAge),
			HttpOnly: true, Secure: w.c.Secure,
		})
	case w.s.cleared:
		for _, n := range []string{cookieName, perlCookieName} {
			http.SetCookie(w.ResponseWriter, &http.Cookie{
				Name: n, Value: "", Path: "/", MaxAge: -1,
				Expires:  time.Unix(0, 0),
				HttpOnly: true, Secure: w.c.Secure, // Perl sets secure on chats too (bin/api.psgi:20)
			})
		}
	}
}

func (w *cookieWriter) WriteHeader(code int) {
	// 1xx informational headers are not final; leave them alone.
	if code >= 200 || code == http.StatusSwitchingProtocols {
		w.setCookies()
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *cookieWriter) Write(b []byte) (int, error) {
	w.setCookies()
	return w.ResponseWriter.Write(b)
}

func (w *cookieWriter) Flush() {
	w.setCookies()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
