// Package passwords hashes and checks passwords (bcrypt, cost 12).
package passwords

import (
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
