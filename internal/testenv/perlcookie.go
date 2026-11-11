package testenv

import "github.com/lierc/lierc-api/internal/session"

// PerlCookie returns a "chats" cookie value logging in user, as
// Plack::Middleware::Session::Cookie writes it: URL-escaped
// "<id>:<base64 Storable nfreeze {user => $user}>:<hmac_sha1_hex>". user must
// be ASCII, which every base62 id is.
func PerlCookie(user, secret string) string {
	v, err := session.WritePerl(user, secret, "1792769372.78598")
	if err != nil {
		panic("testenv.PerlCookie: " + err.Error())
	}
	return v
}
