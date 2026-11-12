package accounts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// Unsubscribe tokens go in notification emails, made by lierc's notifier,
// which has its own copy of this format. They don't expire: the token is
// base64url (unpadded) of "<user>:<mac>", the mac the hex HMAC-SHA256 of
// "unsubscribe|<user>" keyed by a secret used for nothing else.

func unsubscribeMac(secret, user string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("unsubscribe|" + user))
	return hex.EncodeToString(m.Sum(nil))
}

// UnsubscribeToken is user's unsubscribe token.
func UnsubscribeToken(secret, user string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(user + ":" + unsubscribeMac(secret, user)))
}

// CheckUnsubscribeToken is the user a token was made for, if its mac
// checks.
func CheckUnsubscribeToken(secret, token string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", false
	}
	i := strings.LastIndexByte(string(raw), ':')
	if i <= 0 {
		return "", false
	}
	user, mac := string(raw[:i]), raw[i+1:]
	if !hmac.Equal(mac, []byte(unsubscribeMac(secret, user))) {
		return "", false
	}
	return user, true
}
