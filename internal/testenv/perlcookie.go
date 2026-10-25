package testenv

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"net/url"
)

// PerlCookie returns a "chats" cookie value logging in user, as
// Plack::Middleware::Session::Cookie writes it: URL-escaped
// "<id>:<base64 Storable nfreeze {user => $user}>:<hmac_sha1_hex>". user must
// be ASCII and at most 255 bytes, which every base62 id is.
func PerlCookie(user, secret string) string {
	if len(user) > 255 {
		panic("testenv.PerlCookie: user too long")
	}
	b := []byte{0x05, 0x0b, 0x03, 0, 0, 0, 1} // nfreeze magic, SX_HASH, 1 entry
	b = append(b, 0x0a, byte(len(user)))      // SX_SCALAR
	b = append(b, user...)
	b = binary.BigEndian.AppendUint32(b, uint32(len("user")))
	b = append(b, "user"...)
	data := base64.StdEncoding.EncodeToString(b)
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(data))
	return url.QueryEscape("1792769372.78598:" + data + ":" + hex.EncodeToString(mac.Sum(nil)))
}
