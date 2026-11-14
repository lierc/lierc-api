package accounts

import (
	"regexp"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// IsEmail reports whether s is an address the API takes: exactly one "@",
// a local part of [A-Za-z0-9_.+-], and a domain isDomain takes.
func IsEmail(s string) bool {
	parts := strings.Split(s, "@")
	if len(parts) != 2 {
		return false
	}
	return emailLocal.MatchString(parts[0]) && isDomain(parts[1])
}

// Both patterns match the whole value (Go's "$" is the end of the text), so
// a trailing newline is refused.
var (
	emailLocal  = regexp.MustCompile(`^[A-Za-z0-9_.+-]+$`)
	domainLabel = regexp.MustCompile(`^(?:[0-9A-Za-z]|[0-9A-Za-z][0-9A-Za-z-]*[0-9A-Za-z])$`)
)

// isDomain reports a domain name of at least two labels of letters, digits
// and inner hyphens (no underscores), at most 63 bytes a label and 255
// encoded, whose TLD is in the Public Suffix List's ICANN section.
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
	return isTLD(strings.ToLower(labels[len(labels)-1]))
}

// isTLD says whether tld is a top-level domain in the ICANN section of the
// Public Suffix List, which follows IANA's root zone and leaves out the
// reserved names (test, example, invalid, localhost). It asks about a name
// under tld, so a TLD the list has only as a wildcard rule (*.ck) counts.
// The list is embedded in golang.org/x/net; bumping that module updates it.
func isTLD(tld string) bool {
	suffix, icann := publicsuffix.PublicSuffix("x." + tld)
	return icann && (suffix == tld || strings.HasSuffix(suffix, "."+tld))
}
