// Package session reads Perl's Plack::Middleware::Session::Cookie "chats"
// cookie. It never writes one: the Perl API issues and refreshes it.
package session

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"

	"github.com/lierc/lierc-api/internal/storable"
)

const perlCookieName = "chats"

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

// User returns the user logged in by r's "chats" cookie, or "" when there
// is none or it does not verify. As in Plack::Request, the first "chats"
// cookie wins.
func User(r *http.Request, secret string) string {
	c, err := r.Cookie(perlCookieName)
	if err != nil {
		return ""
	}
	user, _ := ReadPerl(c.Value, secret)
	return user
}
