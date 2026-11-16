// Package passwords hashes and checks passwords (bcrypt, cost 12).
package passwords

import (
	"crypto/rand"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// bcrypt ignores everything past 72 bytes and Go's GenerateFromPassword
// refuses it, so passwords are cut here: new ones over 72 bytes are
// refused before this (accounts), and older ones are checked by their
// first 72 bytes, as they were hashed.
const maxBytes = 72

var (
	cost = 12
	// Dummy is a hash at the current cost that nobody has the password for,
	// to check against when a login matches no user, so that case takes as
	// long as the others.
	Dummy = "$2b$12$iTKU3aaMiT3Vi52s2w3Xn.2gy1gF6/6pKUgHg/.dMCAsrp43MwrkG"
)

// Cost is the bcrypt cost Hash uses and NeedsRehash expects: 12, or
// bcrypt.MinCost after UseTestCost.
func Cost() int { return cost }

// UseTestCost switches to bcrypt's minimum cost (and a Dummy at that
// cost), so tests that hash passwords don't spend most of their time in
// bcrypt. For TestMain only (testenv.Main calls it); never call it in
// production code.
func UseTestCost() {
	pw := make([]byte, 32)
	rand.Read(pw)
	h, err := bcrypt.GenerateFromPassword(pw, bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	cost, Dummy = bcrypt.MinCost, string(h)
}

func cut(pw string) []byte {
	if len(pw) > maxBytes {
		pw = pw[:maxBytes]
	}
	return []byte(pw)
}

// Hash returns a bcrypt hash of pw at Cost().
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

// NeedsRehash reports whether hash isn't at Cost() (or isn't a bcrypt hash).
func NeedsRehash(hash string) bool {
	c, err := bcrypt.Cost([]byte(hash))
	return err != nil || c != cost
}
