package lctl

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/passwords"
)

func TestConvertLegacyPasswords(t *testing.T) {
	b, err := os.ReadFile("../../testdata/fixtures/legacy_hashes.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx []struct{ Secret, Password, Legacy string }
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	// the first two fixtures share the secret 0123456789abcdef
	h := newHarness(t)
	h.env.Config.Secret = fx[0].Secret
	a, b2 := h.user("ann"), h.user("bob")
	bad, done := h.user("cat"), h.user("dan")
	h.exec(`UPDATE "user" SET password = $2 WHERE id = $1`, a, fx[0].Legacy)
	h.exec(`UPDATE "user" SET password = $2 WHERE id = $1`, b2, fx[1].Legacy)
	h.exec(`UPDATE "user" SET password = 'abc' WHERE id = $1`, bad)
	h.exec(`UPDATE "user" SET password = NULL, password_hash = $2 WHERE id = $1`, done, "$2b$12$xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")

	if err := h.run("convert-legacy-passwords", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	h.has("converted 2, skipped 1 (malformed), already done 1\n", "cat\n")
	if n := h.count(`SELECT count(*) FROM "user" WHERE password_hash IS NULL`); n != 3 {
		t.Fatalf("dry run wrote: %d users without a hash, want 3", n)
	}

	if err := h.run("convert-legacy-passwords"); err != nil {
		t.Fatal(err)
	}
	h.has("converted 2, skipped 1 (malformed), already done 1\n", "cat\n")
	h.lacks("$2b$15$", fx[0].Legacy)
	for i, u := range []string{a, b2} {
		hash, legacy := h.hash(u)
		if hash == nil || legacy == nil || *legacy != fx[i].Legacy {
			t.Fatalf("user %d: hash %v legacy %v", i, hash, legacy)
		}
		if !passwords.Check(fx[i].Password, *hash) {
			t.Errorf("user %d: fixture password doesn't check", i)
		}
	}
	if hash, legacy := h.hash(bad); hash != nil || legacy == nil || *legacy != "abc" {
		t.Errorf("malformed user changed: %v %v", hash, legacy)
	}
	if hash, _ := h.hash(done); hash == nil || !strings.HasPrefix(*hash, "$2b$12$x") {
		t.Errorf("already-done user changed: %v", hash)
	}

	if err := h.run("convert-legacy-passwords"); err != nil {
		t.Fatal(err)
	}
	h.has("converted 0, skipped 1 (malformed), already done 3\n")
}

func TestConvertLegacyPasswordsLongSecret(t *testing.T) {
	h := newHarness(t)
	h.env.Config.Secret = strings.Repeat("s", 17)
	u := h.user("ann")
	h.exec(`UPDATE "user" SET password = $2 WHERE id = $1`, u, strings.Repeat("ab", 23))
	if err := h.run("convert-legacy-passwords"); err == nil {
		t.Fatal("no error for a secret longer than 16 bytes")
	}
	if hash, _ := h.hash(u); hash != nil {
		t.Error("wrote a hash")
	}
}
