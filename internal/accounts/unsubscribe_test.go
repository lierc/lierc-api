package accounts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

// the vector lierc's notifier test pins too, computed independently
const unsubscribeVector = "dTE6YWVkODQwMTQ4OTJlZGFiZGM4OWFhNDI2MzRhMTQ5YTc1OWZmNjc1ZDMxNzM0YjkzOGMwYmYzYzZjYjAwOTFmMw"

func TestUnsubscribeTokenVector(t *testing.T) {
	if got := UnsubscribeToken("test-secret", "u1"); got != unsubscribeVector {
		t.Errorf("token %s, want %s", got, unsubscribeVector)
	}
}

func signed(secret, msg string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestCheckUnsubscribeToken(t *testing.T) {
	if user, ok := CheckUnsubscribeToken("test-secret", unsubscribeVector); !ok || user != "u1" {
		t.Errorf("vector: %q %v, want u1 true", user, ok)
	}
	changed := []byte(unsubscribeVector)
	changed[len(changed)-1] ^= 1
	for name, tok := range map[string]string{
		"empty":          "",
		"not base64":     "!!!",
		"changed":        string(changed),
		"padded":         unsubscribeVector + "==",
		"other secret":   UnsubscribeToken("other-secret", "u1"),
		"other purpose":  b64("u1:" + signed("test-secret", "verify|u1")),
		"no separator":   b64("u1"),
		"empty user":     b64(":" + signed("test-secret", "unsubscribe|")),
		"other user mac": b64("u2:" + signed("test-secret", "unsubscribe|u1")),
	} {
		if user, ok := CheckUnsubscribeToken("test-secret", tok); ok || user != "" {
			t.Errorf("%s: %q %v, want refused", name, user, ok)
		}
	}
}
