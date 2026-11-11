package accounts

import (
	"regexp"
	"strings"
)

// IsEmail ports Data::Validate::Email::is_email as Auth.pm calls it (no
// options): exactly one "@", a local part of [A-Za-z0-9_.+-], and a domain
// that passes Data::Validate::Domain::is_domain with its defaults.
//
// One difference, on purpose: Perl's split drops trailing empty fields, so
// it takes "a@b.com@". IsEmail refuses it.
func IsEmail(s string) bool {
	parts := strings.Split(s, "@")
	if len(parts) != 2 {
		return false
	}
	return emailLocal.MatchString(parts[0]) && isDomain(parts[1])
}

// Go's "$" is the end of the text, so these are Perl's patterns plus its
// "untainted value equals the input" check, which refuses a trailing
// newline that Perl's "$" would let through.
var (
	emailLocal  = regexp.MustCompile(`^[A-Za-z0-9_.+-]+$`)
	domainLabel = regexp.MustCompile(`^(?:[0-9A-Za-z]|[0-9A-Za-z][0-9A-Za-z-]*[0-9A-Za-z])$`)
)

// isDomain is Data::Validate::Domain::is_domain with default options: no
// underscores, at least two labels, and a TLD that Net::Domain::TLD knows.
func isDomain(d string) bool {
	trailingDot := strings.HasSuffix(d, ".")
	d = strings.TrimSuffix(d, ".")
	encoded := 2 + len(d)
	if trailingDot {
		encoded = 1 + len(d)
	}
	if encoded > 255 {
		return false
	}
	labels := strings.Split(d, ".")
	for _, l := range labels {
		if len(l) > 63 || !domainLabel.MatchString(l) {
			return false
		}
	}
	if len(labels) < 2 {
		return false
	}
	return tlds[strings.ToLower(labels[len(labels)-1])]
}
