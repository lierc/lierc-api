// Package storable reads the tiny subset of Perl's Storable nfreeze format
// that Plack::Middleware::Session::Cookie produces: a flat hash of strings.
package storable

import (
	"encoding/binary"
	"errors"
)

// Storable opcodes (Storable.xs).
const (
	sxLScalar  = 1
	sxHash     = 3
	sxScalar   = 10
	sxUTF8Str  = 23
	sxLUTF8Str = 24
)

var errFormat = errors.New("storable: unsupported or malformed data")

// DecodeHash decodes an nfreeze'd hash of string scalars. Anything else is an error.
func DecodeHash(b []byte) (map[string]string, error) {
	// Header: (major<<1)|netorder, minor. nfreeze => netorder bit set, major 2.
	if len(b) < 2 || b[0] != (2<<1)|1 {
		return nil, errFormat
	}
	b = b[2:]
	if len(b) < 5 || b[0] != sxHash {
		return nil, errFormat
	}
	n := binary.BigEndian.Uint32(b[1:5])
	b = b[5:]
	// Each entry needs at least 1+1 (value) + 4 (key len) bytes.
	if uint64(n)*6 > uint64(len(b)) {
		return nil, errFormat
	}
	out := make(map[string]string, n)
	for i := uint32(0); i < n; i++ {
		var v string
		var err error
		v, b, err = readString(b)
		if err != nil {
			return nil, err
		}
		if len(b) < 4 {
			return nil, errFormat
		}
		kl := binary.BigEndian.Uint32(b)
		b = b[4:]
		if uint64(kl) > uint64(len(b)) {
			return nil, errFormat
		}
		out[string(b[:kl])] = v
		b = b[kl:]
	}
	if len(b) != 0 {
		return nil, errFormat
	}
	return out, nil
}

func readString(b []byte) (string, []byte, error) {
	if len(b) < 1 {
		return "", nil, errFormat
	}
	op := b[0]
	b = b[1:]
	var l uint64
	switch op {
	case sxScalar, sxUTF8Str:
		if len(b) < 1 {
			return "", nil, errFormat
		}
		l = uint64(b[0])
		b = b[1:]
	case sxLScalar, sxLUTF8Str:
		if len(b) < 4 {
			return "", nil, errFormat
		}
		l = uint64(binary.BigEndian.Uint32(b))
		b = b[4:]
	default:
		return "", nil, errFormat
	}
	if l > uint64(len(b)) {
		return "", nil, errFormat
	}
	return string(b[:l]), b[l:], nil
}
