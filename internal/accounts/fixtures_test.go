package accounts

import (
	"encoding/json"
	"os"
	"testing"
)

// authFixtures is testdata/fixtures/auth.json, written by gen.pl from the
// real Perl: API::SignedToken, Data::Validate::Email::is_email and
// Util->irc_nick.
type authFixtures struct {
	Tokens struct {
		Make []struct {
			Secret, Purpose, User, Fingerprint, Token string
			Expires                                   int64
		}
		Valid []struct {
			Name, Secret, Purpose, Token, Fingerprint string
			Now                                       int64
			Valid                                     bool
		}
		Parsing []struct {
			Token string
			User  *string
			Valid bool
		}
	}
	Emails []struct {
		Email string
		Valid bool
	}
	Nicks []struct {
		Username, Nick string
	}
}

func loadAuthFixtures(t *testing.T) authFixtures {
	t.Helper()
	b, err := os.ReadFile("../../testdata/fixtures/auth.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx authFixtures
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Tokens.Make) == 0 || len(fx.Tokens.Valid) == 0 ||
		len(fx.Tokens.Parsing) == 0 || len(fx.Emails) == 0 || len(fx.Nicks) == 0 {
		t.Fatal("auth.json is missing cases")
	}
	return fx
}

func TestTokensMatchPerl(t *testing.T) {
	fx := loadAuthFixtures(t)
	for _, c := range fx.Tokens.Make {
		got := MakeToken(c.Secret, c.Purpose, c.User, c.Fingerprint, c.Expires)
		if got != c.Token {
			t.Errorf("MakeToken(%s, %s, %s): got %q, Perl %q", c.Purpose, c.User, c.Fingerprint, got, c.Token)
		}
		if u, ok := TokenUser(c.Token); !ok || u != c.User {
			t.Errorf("TokenUser(%q) = %q, %v; want %q", c.Token, u, ok, c.User)
		}
	}
	for _, c := range fx.Tokens.Valid {
		if got := TokenValid(c.Secret, c.Purpose, c.Token, c.Fingerprint, c.Now); got != c.Valid {
			t.Errorf("%s: TokenValid = %v, Perl %v", c.Name, got, c.Valid)
		}
	}
	// Odd tokens: Perl's verify purpose, fingerprint and now (see gen.pl).
	const now = 1800000000 - 3600
	for _, c := range fx.Tokens.Parsing {
		u, ok := TokenUser(c.Token)
		switch {
		case c.User == nil && ok:
			t.Errorf("TokenUser(%q) = %q; Perl names no user", c.Token, u)
		case c.User != nil && (!ok || u != *c.User):
			t.Errorf("TokenUser(%q) = %q, %v; Perl %q", c.Token, u, ok, *c.User)
		}
		if got := TokenValid("test-secret", "verify", c.Token, "lee@example.com", now); got != c.Valid {
			t.Errorf("TokenValid(%q) = %v, Perl %v", c.Token, got, c.Valid)
		}
	}
}

func TestIsEmailMatchesPerl(t *testing.T) {
	fx := loadAuthFixtures(t)
	// Perl's split drops trailing empty fields, so it takes these; Go
	// refuses them (the documented fix).
	fixed := map[string]bool{"a@b.com@": true, "a@b.com@@": true}
	seen := 0
	for _, c := range fx.Emails {
		got := IsEmail(c.Email)
		if fixed[c.Email] {
			seen++
			if !c.Valid {
				t.Errorf("%q: Perl now refuses it; drop it from the fixed list", c.Email)
			}
			if got {
				t.Errorf("IsEmail(%q) = true; want false (trailing @ is refused, unlike Perl)", c.Email)
			}
			continue
		}
		if got != c.Valid {
			t.Errorf("IsEmail(%q) = %v, Perl %v", c.Email, got, c.Valid)
		}
	}
	if seen != len(fixed) {
		t.Errorf("fixture has %d of the %d trailing-@ cases", seen, len(fixed))
	}
}

func TestIrcNickMatchesPerl(t *testing.T) {
	fx := loadAuthFixtures(t)
	for _, c := range fx.Nicks {
		if got := IrcNick(c.Username); got != c.Nick {
			t.Errorf("IrcNick(%q) = %q, Perl %q", c.Username, got, c.Nick)
		}
	}
}
