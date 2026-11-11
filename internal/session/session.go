// Package session reads Perl's Plack::Middleware::Session::Cookie "chats"
// cookie, and writes it in the same format (Go refreshes it on its own
// responses; Perl still issues it at login).
package session

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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

// perlExpires is how long the cookie lasts (bin/api.psgi expires), renewed
// on every response.
const perlExpires = 7 * 24 * time.Hour

// PerlID is the cookie's first part as Plack writes it: gettimeofday as
// Perl prints it ("1792769372.78598"). Plack makes a new one on every
// request and never checks it.
func PerlID(now time.Time) string {
	return strconv.FormatFloat(float64(now.UnixMicro())/1e6, 'f', -1, 64)
}

// WritePerl is a "chats" cookie value logging in user, as
// Plack::Middleware::Session::Cookie writes it: URL-escaped
// "<id>:<base64 Storable nfreeze {user => user}>:<hmac_sha1_hex(base64, secret)>".
func WritePerl(user, secret, id string) (string, error) {
	raw, err := storable.EncodeHash(map[string]string{"user": user})
	if err != nil {
		return "", err
	}
	data := base64.StdEncoding.EncodeToString(raw)
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(data))
	return url.QueryEscape(id + ":" + data + ":" + hex.EncodeToString(mac.Sum(nil))), nil
}

// SetPerl sets the "chats" cookie logging in user for another 7 days, with
// the attributes bin/api.psgi gives it.
func SetPerl(w http.ResponseWriter, user, secret string, secure bool, now time.Time) error {
	v, err := WritePerl(user, secret, PerlID(now))
	if err != nil {
		return err
	}
	// a raw header, so the value keeps Plack's escaping exactly
	c := perlCookieName + "=" + v + "; Path=/; Expires=" + now.Add(perlExpires).UTC().Format(http.TimeFormat) + "; HttpOnly; SameSite=Strict"
	if secure {
		c += "; Secure"
	}
	w.Header().Add("Set-Cookie", c)
	return nil
}
