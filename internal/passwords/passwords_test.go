package passwords

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type legacyFixture struct{ Secret, Password, Legacy string }

func fixtures(t *testing.T) []legacyFixture {
	t.Helper()
	b, err := os.ReadFile("../../testdata/fixtures/legacy_hashes.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx []legacyFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx) == 0 {
		t.Fatal("no fixtures")
	}
	return fx
}

func TestFromLegacyFixtures(t *testing.T) {
	for _, f := range fixtures(t) {
		h, err := FromLegacy(f.Secret, f.Legacy)
		if err != nil {
			t.Fatalf("%q: %v", f.Legacy, err)
		}
		if !strings.HasPrefix(h, "$2b$15$") || len(h) != 60 {
			t.Errorf("%q: odd hash shape %d bytes", f.Legacy, len(h))
		}
		if !Check(f.Password, h) {
			t.Errorf("%q: right password refused", f.Legacy)
		}
		if Check("nope", h) {
			t.Errorf("%q: wrong password accepted", f.Legacy)
		}
	}
}

func TestFromLegacyRefuses(t *testing.T) {
	good := strings.Repeat("ab", 23)
	for name, c := range map[string][2]string{
		"long secret": {strings.Repeat("s", 17), good},
		"short hash":  {"changeme", strings.Repeat("a", 45)},
		"long hash":   {"changeme", strings.Repeat("a", 47)},
		"non-hex":     {"changeme", strings.Repeat("g", 46)},
	} {
		if h, err := FromLegacy(c[0], c[1]); err == nil {
			t.Errorf("%s: no error, got %q", name, h)
		}
	}
}

func TestHashCut72(t *testing.T) {
	pw := strings.Repeat("0123456789", 10)
	h, err := Hash(pw)
	if err != nil {
		t.Fatal(err)
	}
	if !Check(pw[:72], h) || !Check(pw, h) {
		t.Error("a 100-byte password should check with its first 72 bytes and with all 100")
	}
	if Check(pw[:71], h) {
		t.Error("71 bytes accepted")
	}
}

func TestCheckRefusesOtherHashes(t *testing.T) {
	for _, h := range []string{"$1$x", ""} {
		if Check("pw", h) {
			t.Errorf("Check accepted %q", h)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	f := fixtures(t)[0]
	conv, err := FromLegacy(f.Secret, f.Legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !NeedsRehash(conv) {
		t.Error("a $2b$15$ hash should need a rehash")
	}
	fresh, err := Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	if NeedsRehash(fresh) {
		t.Error("a fresh hash should not")
	}
	if !NeedsRehash("garbage") {
		t.Error("garbage should need a rehash")
	}
}

func TestDummy(t *testing.T) {
	if Check("anything", Dummy) {
		t.Error("dummy matched")
	}
	if NeedsRehash(Dummy) {
		t.Error("dummy should be cost 12")
	}
}
