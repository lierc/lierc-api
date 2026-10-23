package password_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/password"
)

func TestHashMatchesPerl(t *testing.T) {
	if testing.Short() {
		t.Skip("cost 15 is slow")
	}
	raw, err := os.ReadFile("../../testdata/fixtures/passwords.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct{ Secret, Password, Hash string }
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 15 {
		t.Fatalf("got %d fixtures, want 15", len(rows))
	}
	for _, r := range rows {
		got, err := password.Hash(r.Password, r.Secret)
		if err != nil {
			t.Fatalf("secret %q: %v", r.Secret, err)
		}
		if got != r.Hash {
			t.Errorf("secret %q password %q: got %s want %s", r.Secret, r.Password, got, r.Hash)
		}
	}
}

func TestHashLongSecretFails(t *testing.T) {
	_, err := password.Hash("x", strings.Repeat("s", 17))
	if err == nil || err.Error() != "Salt must be exactly 16 octets long" {
		t.Fatalf("got %v", err)
	}
}
