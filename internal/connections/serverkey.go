package connections

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var digits = regexp.MustCompile(`^\s*(\d+)\s*$`)

// serverKey is the settings liercd connects with, as one string to compare
// (Connection.pm server_key): a saved config may have a string port, 0 or 1
// for a flag, or "" for no username. The nick is one: liercd registers with
// it, so changing it reconnects.
func serverKey(c map[string]any) string {
	text := func(v any) string {
		switch v := v.(type) {
		case nil, map[string]any, []any:
			return ""
		case string:
			return v
		case json.Number:
			return v.String()
		case bool:
			if v {
				return "1"
			}
			return ""
		default:
			return fmt.Sprint(v)
		}
	}
	port := text(c["Port"])
	if m := digits.FindStringSubmatch(port); m != nil {
		port = strings.TrimLeft(m[1], "0")
		if port == "" {
			port = "0"
		}
	}
	flag := func(v any) string {
		if perlTrue(v) {
			return "1"
		}
		return "0"
	}
	return strings.Join([]string{
		strings.TrimSpace(strings.ToLower(text(c["Host"]))),
		port, text(c["Nick"]), text(c["User"]), text(c["Pass"]),
		flag(c["Ssl"]), flag(c["SASL"]),
	}, "\x00")
}

// perlTrue is Perl truthiness of a decoded JSON value.
func perlTrue(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != "" && v != "0"
	case json.Number:
		f, err := v.Float64()
		return err != nil || f != 0
	case int:
		return v != 0
	case float64:
		return v != 0
	default:
		return true
	}
}
