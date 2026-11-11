// Package passwords hashes and checks passwords, and converts the legacy
// hashes of the Perl API (Util::hash_password) into standard bcrypt strings.
package passwords

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	cost = 12
	// bcrypt ignores everything past 72 bytes; Go's GenerateFromPassword
	// refuses it, so passwords are cut here, as Perl's Crypt::Bcrypt does.
	maxBytes = 72

	// Dummy is a cost-12 hash nobody has the password for, to check against
	// when a login matches no user, so that case takes as long as the others.
	Dummy = "$2b$12$iTKU3aaMiT3Vi52s2w3Xn.2gy1gF6/6pKUgHg/.dMCAsrp43MwrkG"
)

var bcryptB64 = base64.NewEncoding("./ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789").WithPadding(base64.NoPadding)

func cut(pw string) []byte {
	if len(pw) > maxBytes {
		pw = pw[:maxBytes]
	}
	return []byte(pw)
}

// Hash returns a cost-12 bcrypt hash of pw.
func Hash(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword(cut(pw), cost)
	return string(h), err
}

// Check reports whether pw matches hash. Only $2a$ and $2b$ hashes are accepted.
func Check(pw, hash string) bool {
	if !strings.HasPrefix(hash, "$2a$") && !strings.HasPrefix(hash, "$2b$") {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), cut(pw)) == nil
}

// NeedsRehash reports whether hash isn't at cost 12 (or isn't a bcrypt hash).
func NeedsRehash(hash string) bool {
	c, err := bcrypt.Cost([]byte(hash))
	return err != nil || c != cost
}

// FromLegacy turns the hex digest Util::hash_password made with secret into
// the equivalent $2b$15$ bcrypt string. The salt there was the secret padded
// on the left with spaces to 16 bytes, and the digest is bcrypt's 23 raw bytes.
func FromLegacy(secret, legacyHex string) (string, error) {
	salt := fmt.Sprintf("%16s", secret)
	if len(salt) > 16 {
		return "", errors.New("secret is longer than 16 bytes")
	}
	if len(legacyHex) != 46 {
		return "", errors.New("legacy hash isn't 46 hex characters")
	}
	raw, err := hex.DecodeString(legacyHex)
	if err != nil {
		return "", errors.New("legacy hash isn't hex")
	}
	return "$2b$15$" + bcryptB64.EncodeToString([]byte(salt)) + bcryptB64.EncodeToString(raw), nil
}
