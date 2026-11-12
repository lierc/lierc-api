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
	// TLDs now come from the Public Suffix List (TestIsEmailTLDs): reserved
	// names Perl's list took are refused, and a newer TLD is taken.
	tldChanged := map[string]bool{
		"a@example.test": false, "a@foo.localhost": false, "a@x.example": false,
		"a@x.invalid": false, "a@x.inc": true,
	}
	seen := 0
	for _, c := range fx.Emails {
		got := IsEmail(c.Email)
		if want, ok := tldChanged[c.Email]; ok {
			if got != want || c.Valid == want {
				t.Errorf("IsEmail(%q) = %v, want %v (Perl %v)", c.Email, got, want, c.Valid)
			}
			continue
		}
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

// The TLD comes from the Public Suffix List (golang.org/x/net/publicsuffix),
// which tracks IANA's root zone, not Perl's frozen Net::Domain::TLD list.
func TestIsEmailTLDs(t *testing.T) {
	for addr, want := range map[string]bool{
		"a@x.com":       true,
		"a@x.uk":        true,
		"a@x.co.uk":     true,
		"a@x.africa":    true, // newer than Perl's list
		"a@x.music":     true,
		"a@x.llc":       true,
		"a@x.inc":       true,
		"a@x.AFRICA":    true,
		"a@x.ck":        true, // a wildcard rule (*.ck) in the list
		"a@x.xn--p1ai":  true,
		"a@x.test":      false, // reserved (RFC 2606), which Perl's list took
		"a@x.example":   false,
		"a@x.invalid":   false,
		"a@x.localhost": false,
		"a@x.yu":        false, // retired
		"a@x.an":        false,
		"a@x.zz":        false,
		"a@x.github.io": true, // a private suffix still ends in a real TLD
		"a@x.io.":       true,
	} {
		if got := IsEmail(addr); got != want {
			t.Errorf("IsEmail(%q) = %v, want %v", addr, got, want)
		}
	}
}
