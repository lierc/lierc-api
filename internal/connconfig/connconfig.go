// Package connconfig is the rules the API applies to an IRC connection's
// settings, and the check that refuses hosts pointing at loopback or private
// addresses. It began as a copy of the Perl API's lib/API/ConnectionConfig.pm
// (now only in git history) and keeps its rules, quirks included: `\s` is
// Unicode whitespace and `$` also matches before a final newline. It doesn't
// follow Perl's type conversions: text fields must be JSON strings, Port a
// JSON number, Ssl/SASL JSON booleans, Channels/Highlight lists of strings
// (null counts as missing). testdata/connection-config-cases.json, generated
// from the Perl code and now edited by hand, is the record of those rules:
// a rule changed here must change there too.
package connconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	foldcase "golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/lierc/lierc-api/internal/config"
)

// perlSpace is Perl's Unicode \s (the White_Space property) as a character
// class body; Go's \s is ASCII only.
const perlSpace = `\t\n\v\f\r \x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

// Perl's `$` (end, or before a final newline) is written `\n?$` below.

// A nick follows Ergo's rule under casemapping "permissive" (see
// lib/API/ConnectionConfig.pm): letters, numbers, punctuation and symbols
// only, none of nickForbidden, not starting with # ~ & @ % + - or $, at most
// 32 bytes before and after folding, and not a name Ergo reserves.
const nickForbidden = ` ,*?.!@:<>'";#/`

var nickReserved = map[string]bool{}

func init() {
	for _, n := range []string{"0", "NickServ", "ChanServ", "HostServ", "HistServ", "MemoServ", "BotServ", "OperServ", "Global", "=scene="} {
		nickReserved[foldcase.Fold().String(n)] = true
	}
}

var (
	nickRe = regexp.MustCompile(`^[^#~&@%+\-$][\pL\pN\pP\pS]*$`)
	// any characters but space, NUL, CR, LF and @, not starting with :
	userRe    = regexp.MustCompile(`^[^ \x00\r\n@:][^ \x00\r\n@]{0,31}$`)
	channelRe = regexp.MustCompile(`^[#&+!][^` + perlSpace + `,\x00-\x1f\x7f]{1,199}\n?$`)
	aliasRe   = regexp.MustCompile(`^[A-Za-z0-9 .-]{1,32}\n?$`)
	portRe    = regexp.MustCompile(`^[ \t\n\r\f\v]*([0-9]{1,5})[ \t\n\r\f\v]*\n?$`)
	ipv4Re    = regexp.MustCompile(`^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\n?$`)
	labelRe   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\n?$`)
	digitsRe  = regexp.MustCompile(`^\pN+\n?$`) // Perl \d without /a: any Unicode digit
	controlRe = regexp.MustCompile(`[\x00-\x1f\x7f]`)
)

const (
	errNick     = "Nick can only use letters, numbers, punctuation and symbols, not spaces or , * ? . ! @ : < > ' \" ; # /, and can't start with ~ & % + - or $"
	errNickLong = "Nick must be at most 32 bytes; accented letters and emoji take 2 to 4 each"
	errUser     = "Username must be up to 32 characters, without spaces or @, and can't start with :"
	errAlias    = "Network alias can only use letters, digits, spaces, dots and hyphens, up to 32 characters"
)

func trim(s string) string { return strings.TrimFunc(s, unicode.IsSpace) }

// str is an optional text field: present false for missing or null, an
// error naming label if it isn't a string; the value is trimmed.
func str(v any, label string) (s string, present bool, err error) {
	if v == nil {
		return "", false, nil
	}
	x, ok := v.(string)
	if !ok {
		return "", false, errors.New(label + " must be a string")
	}
	return x, true, nil
}

// list is Perl's list() for a list of strings: the trimmed, non-blank
// items.
func list(v any, name string) ([]string, error) {
	out := []string{}
	if v == nil {
		return out, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, errors.New(name + " must be a list of strings")
	}
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, errors.New(name + " must be a list of strings")
		}
		if s = trim(s); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// boolean is an optional JSON boolean; missing or null is false.
func boolean(v any, name string) (bool, error) {
	if v == nil {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, errors.New(name + " must be true or false")
	}
	return b, nil
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

	host, ok, err := str(params["Host"], "Host")
	if err != nil {
		return nil, err
	}
	if host = trim(host); !ok || host == "" {
		return nil, errors.New("Host is required")
	}
	if !validHost(host) {
		return nil, errors.New("Host must be an IPv4 address or a domain name")
	}
	config["Host"] = host

	port := 0
	switch p := params["Port"].(type) {
	case nil:
	case json.Number:
		f, err := strconv.ParseFloat(string(p), 64)
		if err == nil && f == math.Trunc(f) && f >= 1 && f <= 65535 {
			port = int(f)
		}
	default:
		return nil, errors.New("Port must be a number")
	}
	if port == 0 {
		return nil, errors.New("Port must be a number from 1 to 65535")
	}
	config["Port"] = port

	nick, _, err := str(params["Nick"], "Nick")
	if err != nil {
		return nil, err
	}
	if nick = trim(nick); nick == "" {
		return nil, errors.New("Nick is required")
	}
	if !nickRe.MatchString(nick) || strings.ContainsAny(nick, nickForbidden) {
		return nil, errors.New(errNick)
	}
	// Ergo's folding: NFD, case fold, NFD
	if len(nick) > 32 || len(norm.NFD.String(foldcase.Fold().String(norm.NFD.String(nick)))) > 32 {
		return nil, errors.New(errNickLong)
	}
	if nickReserved[foldcase.Fold().String(nick)] {
		return nil, fmt.Errorf("Nick %s is reserved", nick)
	}
	config["Nick"] = nick

	user, _, err := str(params["User"], "Username")
	if err != nil {
		return nil, err
	}
	if user = trim(user); user != "" {
		if !userRe.MatchString(user) {
			return nil, errors.New(errUser)
		}
		config["User"] = user
	}

	pass, _, err := str(params["Pass"], "Password")
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(pass, "\r\n\x00") {
		return nil, errors.New("Password can't contain line breaks")
	}
	if pass != "" {
		config["Pass"] = pass
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

	names, err := CleanNames(params)
	if err != nil {
		return nil, err
	}
	for k, v := range names {
		config[k] = v
	}

	for _, k := range []string{"Ssl", "SASL"} {
		b, err := boolean(params[k], k)
		if err != nil {
			return nil, err
		}
		config[k] = b
	}
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
	return strings.ToLower(host) == strings.ToLower(def) && port == config.DefaultNetworkPort(getenv("DEFAULT_NETWORK_PORT"))
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

// Display is a decoded JSON scalar as text for messages (a string as is,
// a number as written), ok false for anything else.
func Display(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return string(x), true
	}
	return "", false
}

// CleanNames checks and normalises the settings that don't need a
// reconnect (API::ConnectionConfig clean_names): Highlight, always set,
// and Alias, set unless blank. Clean uses it for those keys.
func CleanNames(params map[string]any) (map[string]any, error) {
	names := map[string]any{}
	highlight, err := list(params["Highlight"], "Highlight")
	if err != nil {
		return nil, err
	}
	for _, h := range highlight {
		if controlRe.MatchString(h) {
			return nil, errors.New("Highlight words can't contain control characters")
		}
	}
	names["Highlight"] = highlight

	alias, _, err := str(params["Alias"], "Network alias")
	if err != nil {
		return nil, err
	}
	if alias = trim(alias); alias != "" {
		if !aliasRe.MatchString(alias) {
			return nil, errors.New(errAlias)
		}
		names["Alias"] = alias
	}
	return names, nil
}
