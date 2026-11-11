package login

import (
	"net/http"
	"time"
)

// SetCookie sets the "chats" cookie to token, expiring a Lifetime after now.
func SetCookie(w http.ResponseWriter, token string, secure bool, now time.Time) {
	c := CookieName + "=" + token + "; Path=/; Expires=" + now.Add(Lifetime).UTC().Format(http.TimeFormat) + "; HttpOnly; SameSite=Strict"
	if secure {
		c += "; Secure"
	}
	w.Header().Add("Set-Cookie", c)
}

// ExpireCookie tells the browser to drop the "chats" cookie (logout).
func ExpireCookie(w http.ResponseWriter, secure bool) {
	c := CookieName + "=; Path=/; Expires=" + time.Unix(0, 0).UTC().Format(http.TimeFormat) + "; Max-Age=0; HttpOnly; SameSite=Strict"
	if secure {
		c += "; Secure"
	}
	w.Header().Add("Set-Cookie", c)
}
