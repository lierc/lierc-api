// Package password reproduces Util->hash_password from the Perl service.
package password

import (
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/crypto/blowfish"
)

const cost = 15

// magic is the bcrypt plaintext, encrypted 64 times.
var magic = []byte("OrpheanBeholderScryDoubt")

// Hash returns the lowercase hex of the 23-byte bcrypt digest (cost 15) of
// password, using the raw salt sprintf("%16s", secret). Perl's Digest::Bcrypt
// requires a salt of exactly 16 bytes, so a secret longer than 16 bytes fails
// the same way (lib/Util.pm hash_password).
func Hash(password, secret string) (string, error) {
	// sprintf("%16s") left-pads with spaces and never truncates.
	salt := fmt.Sprintf("%16s", secret)
	if len(salt) != 16 {
		return "", errors.New("Salt must be exactly 16 octets long")
	}

	// 2a: key is the password plus a NUL, at most 72 bytes.
	key := append([]byte(password), 0)
	if len(key) > 72 {
		key = key[:72]
	}

	c, err := blowfish.NewSaltedCipher(key, []byte(salt))
	if err != nil {
		return "", err
	}
	for i := 0; i < 1<<cost; i++ {
		blowfish.ExpandKey(key, c)
		blowfish.ExpandKey([]byte(salt), c)
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
