package testenv

import (
	"encoding/json"
	"os"
	"testing"
)

// The cookie for "lee" must match the one real Perl wrote (login_ascii).
func TestPerlCookieMatchesPerl(t *testing.T) {
	b, err := os.ReadFile("../../testdata/fixtures/cookies.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name  string `json:"name"`
		Value string `json:"cookie_value"`
	}
	if err := json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		if f.Name == "login_ascii" {
			if got := PerlCookie("lee", "test-secret"); got != f.Value {
				t.Fatalf("got  %s\nwant %s", got, f.Value)
			}
			return
		}
	}
	t.Fatal("no login_ascii fixture")
}
