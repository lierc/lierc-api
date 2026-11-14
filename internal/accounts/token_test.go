package accounts

import (
	"encoding/base64"
	"strconv"
	"testing"
	"time"
)

func TestTokenStrictParsing(t *testing.T) {
	const secret, purpose, user, fp = "s", "verify", "u1", "f"
	exp := time.Now().Unix() + 60
	now := time.Now().Unix()
	tok := MakeToken(secret, purpose, user, fp, exp)
	if !TokenValid(secret, purpose, tok, fp, now) {
		t.Fatalf("a fresh token isn't valid")
	}
	if TokenValid(secret, purpose, tok+"A", fp, now) {
		t.Errorf("a token with a lone extra final character is valid")
	}
	e := strconv.FormatInt(exp, 10) + "\n"
	nl := base64.RawURLEncoding.EncodeToString([]byte(user + ":" + e + ":" + tokenMac(secret, purpose, user, e, fp)))
	if TokenValid(secret, purpose, nl, fp, now) {
		t.Errorf("a token whose expiry ends in a newline is valid")
	}
}
