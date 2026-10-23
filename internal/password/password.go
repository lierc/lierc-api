// Package password reproduces Util->hash_password from the Perl service.
package password

import (
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/blowfish"
)

const cost = 15

// magic is the bcrypt plaintext, encrypted 64 times.
var magic = []byte("OrpheanBeholderScryDoubt")

// saltFor builds sprintf("%16s", secret) as Perl does on an undecoded byte
// string: left-pad with spaces by bytes, never truncate.
func saltFor(secret string) ([]byte, error) {
	if len(secret) > 16 {
		return nil, errors.New("Salt must be exactly 16 octets long")
	}
	return []byte(strings.Repeat(" ", 16-len(secret)) + secret), nil
}

// Hash returns the lowercase hex of the 23-byte bcrypt digest (cost 15) of
// password, using the raw salt sprintf("%16s", secret). Perl's Digest::Bcrypt
// requires a salt of exactly 16 bytes, so a secret longer than 16 bytes fails
// the same way (lib/Util.pm hash_password).
func Hash(password, secret string) (string, error) {
	salt, err := saltFor(secret)
	if err != nil {
		return "", err
	}

	// 2a: key is the password plus a NUL, at most 72 bytes.
	key := append([]byte(password), 0)
	if len(key) > 72 {
		key = key[:72]
	}

	c, err := blowfish.NewSaltedCipher(key, salt)
	if err != nil {
		return "", err
	}
	for i := 0; i < 1<<cost; i++ {
		blowfish.ExpandKey(key, c)
		blowfish.ExpandKey(salt, c)
	}

	out := make([]byte, len(magic))
	copy(out, magic)
	for i := 0; i < 64; i++ {
		for j := 0; j < len(out); j += 8 {
			c.Encrypt(out[j:j+8], out[j:j+8])
		}
	}
	return hex.EncodeToString(out[:23]), nil
}
