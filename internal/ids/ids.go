// Package ids generates the base-62 identifiers Perl's Util->uuid produces.
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"strings"
)

// Math::BaseConvert digit order for base 62 (digit set b64 truncated to 62
// digits: 0-9, A-Z, a-z), as the cnv fixtures show.
const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// FromHex converts a hex string to base 62 (cnv($hex, 16, 62)).
func FromHex(h string) string {
	h = strings.TrimPrefix(strings.TrimPrefix(h, "0x"), "0X")
	n, ok := new(big.Int).SetString(h, 16)
	if !ok || n.Sign() == 0 {
		return "0"
	}
	base := big.NewInt(62)
	mod := new(big.Int)
	var out []byte
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		out = append(out, digits[mod.Int64()])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// New returns a random id, like Util->uuid (Data::UUID create_hex converted
// to base 62).
func New() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return FromHex(strings.ToUpper(hex.EncodeToString(b)))
}
