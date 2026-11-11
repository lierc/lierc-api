package accounts

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

// Signed, self-expiring tokens for emailed links, a port of
// lib/API/SignedToken.pm. The token is base64url (unpadded) of
// "<user>:<expires>:<mac>"; the mac is the hex HMAC-SHA256 of
// "purpose|user|expires|fingerprint" keyed by the secret.

// Token lifetimes in seconds, as in SignedToken.pm.
const (
	ResetLifetime  = 3600
	VerifyLifetime = 30 * 86400
)

var (
	tokenChars = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	// Perl's /^\d+$/: "$" also matches before one trailing newline.
	tokenExpires = regexp.MustCompile(`^[0-9]+\n?$`)
)

func tokenMac(secret, purpose, user, expires, fingerprint string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strings.Join([]string{purpose, user, expires, fingerprint}, "|")))
	return hex.EncodeToString(m.Sum(nil))
}

// MakeToken is SignedToken->make.
func MakeToken(secret, purpose, user, fingerprint string, expires int64) string {
	e := strconv.FormatInt(expires, 10)
	return base64.RawURLEncoding.EncodeToString(
		[]byte(user + ":" + e + ":" + tokenMac(secret, purpose, user, e, fingerprint)))
}

// tokenParts is SignedToken.pm's parts: user, expires and mac, as strings.
func tokenParts(token string) (user, expires, mac string, ok bool) {
	if !tokenChars.MatchString(token) {
		return "", "", "", false
	}
	// Perl's decode_base64url pads and decodes leniently: a lone final
	// character (length 1 mod 4) carries no whole byte and is ignored.
	if len(token)%4 == 1 {
		token = token[:len(token)-1]
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", "", "", false
	}
	parts := strings.Split(string(b), ":")
	if len(parts) != 3 || parts[0] == "" || !tokenExpires.MatchString(parts[1]) {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// TokenUser is SignedToken->user: the user id the token names, unverified,
// for looking the user up.
func TokenUser(token string) (string, bool) {
	user, _, _, ok := tokenParts(token)
	return user, ok
}

// TokenValid is SignedToken->valid: the token parses, now isn't after its
// expiry, and its mac matches for this purpose and fingerprint.
func TokenValid(secret, purpose, token, fingerprint string, now int64) bool {
	user, expires, mac, ok := tokenParts(token)
	if !ok || after(now, expires) {
		return false
	}
	want := tokenMac(secret, purpose, user, expires, fingerprint)
	return subtle.ConstantTimeCompare([]byte(mac), []byte(want)) == 1
}

// after reports whether now > expires, where expires is a string of digits
// (perhaps with leading zeros, a trailing newline, or more digits than an
// int64 holds), compared as numbers without overflow.
func after(now int64, expires string) bool {
	if now < 0 {
		return false
	}
	e := strings.TrimLeft(strings.TrimSuffix(expires, "\n"), "0")
	n := strconv.FormatInt(now, 10)
	if n == "0" {
		n = ""
	}
	if len(n) != len(e) {
		return len(n) > len(e)
	}
	return n > e
}
