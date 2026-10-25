package storable

import (
	"encoding/base64"
	"testing"
)

func dec(t *testing.T, s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeHash(t *testing.T) {
	m, err := DecodeHash(dec(t, "BQsDAAAAAQoDbGVlAAAABHVzZXI="))
	if err != nil || len(m) != 1 || m["user"] != "lee" {
		t.Fatalf("got %v %v", m, err)
	}
	m, err = DecodeHash(dec(t, "BQsDAAAAARcNcMOkc3N3w7ZyZOKYugAAAAR1c2Vy"))
	if err != nil || m["user"] != "pässwörd☺" {
		t.Fatalf("got %v %v", m, err)
	}
	m, err = DecodeHash(dec(t, "BQsDAAAAAA=="))
	if err != nil || len(m) != 0 {
		t.Fatalf("got %v %v", m, err)
	}
	// long scalar
	m, err = DecodeHash([]byte{5, 11, 3, 0, 0, 0, 1, 1, 0, 0, 0, 2, 'h', 'i', 0, 0, 0, 1, 'k'})
	if err != nil || m["k"] != "hi" {
		t.Fatalf("got %v %v", m, err)
	}
}

func TestDecodeHashRejects(t *testing.T) {
	for name, b := range map[string][]byte{
		"nil":       nil,
		"header":    {5},
		"array":     {5, 11, 2, 0, 0, 0, 0},
		"truncated": dec(t, "BQsDAAAAAQoDbGVl"),
		"huge":      {5, 11, 3, 255, 255, 255, 255},
		"trailing":  append(dec(t, "BQsDAAAAAA=="), 0),
		"notnet":    {4, 11, 3, 0, 0, 0, 0},
		"integer":   {5, 11, 3, 0, 0, 0, 1, 8, 130, 0, 0, 0, 1, 'k'},
	} {
		if _, err := DecodeHash(b); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func FuzzDecodeHash(f *testing.F) {
	f.Add([]byte{5, 11, 3, 0, 0, 0, 1, 10, 1, 'a', 0, 0, 0, 1, 'k'})
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = DecodeHash(b) })
}
