package ids_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/ids"
)

const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

type fixture struct {
	Cnv         []struct{ Hex, B62 string }
	UUIDSamples []string `json:"uuid_samples"`
}

func load(t *testing.T) fixture {
	raw, err := os.ReadFile("../../testdata/fixtures/ids.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFromHexMatchesPerl(t *testing.T) {
	f := load(t)
	if len(f.Cnv) != 20 {
		t.Fatalf("got %d fixtures", len(f.Cnv))
	}
	for _, r := range f.Cnv {
		if got := ids.FromHex(r.Hex); got != r.B62 {
			t.Errorf("%s: got %s want %s", r.Hex, got, r.B62)
		}
		if got := ids.FromHex(strings.ToUpper(r.Hex)); got != r.B62 {
			t.Errorf("%s upper: got %s want %s", r.Hex, got, r.B62)
		}
	}
}

func TestNewShape(t *testing.T) {
	f := load(t)
	// 2^128 < 62^22, so ids are at most 22 characters. Small values are
	// shorter, as in Perl (no padding): about 13% are under 22 characters.
	minLen, maxLen := 1, 0
	for _, s := range f.UUIDSamples {
		maxLen = max(maxLen, len(s))
	}
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := ids.New()
		if len(id) < minLen || len(id) > maxLen {
			t.Fatalf("%q length outside %d..%d", id, minLen, maxLen)
		}
		for _, c := range id {
			if !strings.ContainsRune(alphabet, c) {
				t.Fatalf("%q has bad char %q", id, c)
			}
		}
		if seen[id] {
			t.Fatalf("duplicate %q", id)
		}
		seen[id] = true
	}
}
