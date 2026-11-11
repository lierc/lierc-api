package storable

import (
	"encoding/binary"
	"errors"
)

// EncodeHash is Storable nfreeze of a hash with one ASCII string value, the
// only shape Plack::Middleware::Session::Cookie needs here ({user => id}).
// A value over 255 bytes is an SX_LSCALAR, as Storable writes it.
func EncodeHash(h map[string]string) ([]byte, error) {
	if len(h) != 1 {
		return nil, errors.New("storable: only one key is supported")
	}
	b := []byte{(2 << 1) | 1, 11, sxHash, 0, 0, 0, 1}
	for k, v := range h {
		for i := 0; i < len(v); i++ {
			if v[i] >= 0x80 {
				return nil, errors.New("storable: only ASCII values are supported")
			}
		}
		if len(v) <= 255 {
			b = append(b, sxScalar, byte(len(v)))
		} else {
			b = append(b, sxLScalar)
			b = binary.BigEndian.AppendUint32(b, uint32(len(v)))
		}
		b = append(b, v...)
		b = binary.BigEndian.AppendUint32(b, uint32(len(k)))
		b = append(b, k...)
	}
	return b, nil
}
