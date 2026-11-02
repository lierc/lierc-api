// Package connconfig is the Go copy of lib/API/ConnectionConfig.pm: the rules
// the API applies to an IRC connection's settings, and the check that refuses
// hosts pointing at loopback or private addresses. lctl uses it to judge saved
// connections exactly as the API judged them, so it reproduces Perl's
// behaviour, quirks included: Ssl/SASL use Perl truthiness, `\s` is Unicode
// whitespace and `$` also matches before a final newline. One deliberate
// difference: text fields must be JSON strings (Perl also takes numbers);
// only Port may be a number, and it's read the way Perl prints it.
// testdata/connection-config-cases.json is run by both test suites (Go's own
// result is under "go" where it's stricter); a rule changed here must change
// there too.
package connconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// perlSpace is Perl's Unicode \s (the White_Space property) as a character
// class body; Go's \s is ASCII only.
const perlSpace = `\t\n\v\f\r \x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

// Perl's `$` (end, or before a final newline) is written `\n?$` below.
const nickChars = "A-Za-z0-9\\[\\]\\\\`_^{|}-"
const nickFirst = "A-Za-z\\[\\]\\\\`_^{|}"

var (
	nickRe    = regexp.MustCompile(`^[` + nickFirst + `][` + nickChars + `]{0,31}\n?$`)
	userRe    = regexp.MustCompile(`^[.~` + nickChars + `]{1,32}\n?$`)
	channelRe = regexp.MustCompile(`^[#&+!][^` + perlSpace + `,\x00-\x1f\x7f]{1,199}\n?$`)
	aliasRe   = regexp.MustCompile(`^[A-Za-z0-9 ]{1,32}\n?$`)
	portRe    = regexp.MustCompile(`^[ \t\n\r\f\v]*([0-9]{1,5})[ \t\n\r\f\v]*\n?$`)
	ipv4Re    = regexp.MustCompile(`^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\n?$`)
	labelRe   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\n?$`)
	digitsRe  = regexp.MustCompile(`^\pN+\n?$`) // Perl \d without /a: any Unicode digit
	controlRe = regexp.MustCompile(`[\x00-\x1f\x7f]`)
)

const (
	errNick  = "Nick must be up to 32 letters, digits, - or [ ] \\ ` _ ^ { | }, not starting with a digit or -"
	errUser  = "Username must be up to 32 letters, digits, - . ~ or [ ] \\ ` _ ^ { | }"
	errAlias = "Network alias can only use letters, digits and spaces, up to 32 characters"
)

// kind is how Perl sees a decoded JSON value.
type kind int

const (
	undef  kind = iota // missing or null
	ref                // true/false (JSON::XS booleans are objects), array, object
	scalar             // string or number
)

// perlValue returns how Perl sees v and, for a scalar, its text as Perl
// prints it.
func perlValue(v any) (kind, string) {
	switch x := v.(type) {
	case nil:
		return undef, ""
	case string:
		return scalar, x
	case json.Number:
		return scalar, perlNumber(string(x))
	default:
		return ref, ""
	}
}

var intLiteral = regexp.MustCompile(`^-?[0-9]+$`)

// perlNumber is how Perl prints a number JSON::XS decoded: an integer
// literal as an integer (one too big for 64 bits is kept as its literal
// text), anything else as a double with %.15g, overflow as Inf or -Inf.
func perlNumber(lit string) string {
	if intLiteral.MatchString(lit) {
		if i, err := strconv.ParseInt(lit, 10, 64); err == nil {
			return strconv.FormatInt(i, 10)
		}
		if u, err := strconv.ParseUint(lit, 10, 64); err == nil {
			return strconv.FormatUint(u, 10)
		}
		return lit
	}
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return lit
	}
	switch {
	case math.IsInf(f, 1):
		return "Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	case f == 0:
		return "0"
	}
	return strconv.FormatFloat(f, 'g', 15, 64)
}

func trim(s string) string { return strings.TrimFunc(s, unicode.IsSpace) }

// textKind is how a text field sees v: unlike Perl, lctl requires a real
// JSON string there, so a number counts as a reference (like a boolean).
// Only Port takes a number.
func textKind(v any) (kind, string) {
	if _, ok := v.(json.Number); ok {
		return ref, ""
	}
	return perlValue(v)
}

// text is Perl's text(): the trimmed string of a string, ok false otherwise.
func text(v any) (string, bool) {
	k, s := textKind(v)
	if k != scalar {
		return "", false
	}
	return trim(s), true
}

// list is Perl's list(): the trimmed, non-blank strings of an optional list.
// Every element is checked before any is used, as Perl's map runs first.
func list(v any, name string) ([]string, error) {
	out := []string{}
	if v == nil {
		return out, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, errors.New(name + " must be a list")
	}
	var all []string
	for _, item := range items {
		k, _ := textKind(item)
		if k != scalar {
			return nil, errors.New(name + " must be a list")
		}
		s, _ := text(item)
		all = append(all, s)
	}
	for _, s := range all {
		if s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// truthy is Perl truthiness for a decoded JSON value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != "" && x != "0"
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return true
		}
		return f != 0
	default:
		return true
	}
}

func ipv4Literal(host string) bool {
	m := ipv4Re.FindStringSubmatch(host)
	if m == nil {
		return false
	}
	for _, p := range m[1:] {
		if n, _ := strconv.Atoi(p); n > 255 {
			return false
		}
	}
	return true
}

func validHost(host string) bool {
	if ipv4Literal(host) {
		return true
	}
	if utf8.RuneCountInString(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	if digitsRe.MatchString(labels[len(labels)-1]) {
		return false // numeric but not a valid IPv4 address
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return false
		}
	}
	return true
}

// Clean checks and normalises a connection's settings (the raw JSON request
// body or saved config), returning only the known keys, or the first
// problem as an error whose message is the API's.
func Clean(raw []byte) (map[string]any, error) {
	v, err := Decode(raw)
	if err != nil {
		return nil, errors.New("Invalid connection settings")
	}
	params, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("Host is required")
	}
	config := map[string]any{}

	rawHost, present := params["Host"]
	host, isText := text(rawHost)
	if !present || rawHost == nil || (isText && host == "") {
		return nil, errors.New("Host is required")
	}
	if !isText || !validHost(host) {
		return nil, errors.New("Host must be an IPv4 address or a domain name")
	}
	config["Host"] = host

	portKind, portText := perlValue(params["Port"])
	m := portRe.FindStringSubmatch(portText)
	port := 0
	if m != nil {
		port, _ = strconv.Atoi(m[1])
	}
	if portKind != scalar || m == nil || port < 1 || port > 65535 {
		return nil, errors.New("Port must be a number from 1 to 65535")
	}
	config["Port"] = port

	nick, ok := text(params["Nick"])
	if !ok || nick == "" {
		return nil, errors.New("Nick is required")
	}
	if !nickRe.MatchString(nick) {
		return nil, errors.New(errNick)
	}
	config["Nick"] = nick

	if rawUser := params["User"]; rawUser != nil {
		user, ok := text(rawUser)
		if !(ok && user == "") {
			if !ok || !userRe.MatchString(user) {
				return nil, errors.New(errUser)
			}
			config["User"] = user
		}
	}

	if rawPass := params["Pass"]; rawPass != nil {
		k, pass := textKind(rawPass)
		if k == ref {
			return nil, errors.New("Password must be text")
		}
		if strings.ContainsAny(pass, "\r\n\x00") {
			return nil, errors.New("Password can't contain line breaks")
		}
		if pass != "" {
			config["Pass"] = pass
		}
	}

	channels, err := list(params["Channels"], "Channels")
	if err != nil {
		return nil, err
	}
	for _, c := range channels {
		if !channelRe.MatchString(c) {
			return nil, errors.New(c + " isn't a valid channel name")
		}
	}
	config["Channels"] = channels

	highlight, err := list(params["Highlight"], "Highlight")
	if err != nil {
		return nil, err
	}
	for _, h := range highlight {
		if controlRe.MatchString(h) {
			return nil, errors.New("Highlight words can't contain control characters")
		}
	}
	config["Highlight"] = highlight

	if rawAlias := params["Alias"]; rawAlias != nil {
		alias, ok := text(rawAlias)
		if !(ok && alias == "") {
			if !ok || !aliasRe.MatchString(alias) {
				return nil, errors.New(errAlias)
			}
			config["Alias"] = alias
		}
	}

	config["Ssl"] = truthy(params["Ssl"])
	config["SASL"] = truthy(params["SASL"])
	return config, nil
}

// Addresses liercd must not connect to. The same list is in
// lib/API/ConnectionConfig.pm and lierc's pkg/lierc/dialguard.go; keep the
// three in step.
var blocked = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4",
		"240.0.0.0/4",
		"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "64:ff9b::/96",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// BlockedIP reports whether liercd must not connect to ip. Anything that
// isn't an address (including one with a zone, which Perl's inet_pton
// rejects) is blocked; an IPv4-mapped address is checked as the IPv4
// address it carries.
func BlockedIP(ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil || a.Zone() != "" {
		return true
	}
	if strings.Contains(ip, ":") != a.Is6() {
		return true // Perl picks the family by the presence of ':'
	}
	a = a.Unmap()
	for _, p := range blocked {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Exempt reports whether host and port are the default network
// (DEFAULT_NETWORK_HOST on DEFAULT_NETWORK_PORT, default 6667), which may be
// private, e.g. irc.relaychat.party -> 127.0.0.1 in /etc/hosts.
func Exempt(getenv func(string) string, host string, port int) bool {
	def := getenv("DEFAULT_NETWORK_HOST")
	if def == "" {
		return false
	}
	p := getenv("DEFAULT_NETWORK_PORT")
	if p == "" || p == "0" { // Perl's ||
		p = "6667"
	}
	return strings.ToLower(host) == strings.ToLower(def) && float64(port) == perlInt(p)
}

var numPrefix = regexp.MustCompile(`^[ \t\n\r\f\v]*([+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?)`)

// perlInt is Perl's int() of a string: its leading number, truncated; 0 if
// it doesn't start with one.
func perlInt(s string) float64 {
	m := numPrefix.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	return math.Trunc(f)
}

// A Resolver returns the addresses a host name resolves to; an error or no
// addresses means it doesn't resolve.
type Resolver func(ctx context.Context, host string) ([]string, error)

// SystemResolver resolves with the system's resolver (including
// /etc/hosts), as the API's getaddrinfo does.
func SystemResolver(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// CheckHost returns an error unless host and port are exempt or every
// address host resolves to is allowed. An IPv4 literal is checked without
// resolving.
func CheckHost(ctx context.Context, getenv func(string) string, resolve Resolver, host string, port int) error {
	if Exempt(getenv, host, port) {
		return nil
	}
	var ips []string
	if ipv4Literal(host) {
		ips = []string{host}
	} else if got, err := resolve(ctx, host); err == nil {
		ips = got
	}
	if len(ips) == 0 {
		return errors.New("Couldn't find host " + host)
	}
	for _, ip := range ips {
		if BlockedIP(ip) {
			return errors.New("Host " + host + " points to a private address")
		}
	}
	return nil
}

// Decode decodes a JSON config keeping numbers as json.Number, as Clean
// sees them.
func Decode(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}

// PerlText is a decoded JSON value as Perl prints it, with ok false when
// Perl would see undef or a reference (null, booleans, arrays, objects).
func PerlText(v any) (string, bool) {
	k, s := perlValue(v)
	return s, k == scalar
}
