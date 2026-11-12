package connections

import (
	"encoding/json"
	"strconv"
	"strings"
)

// serverKey is the settings liercd connects with, as one string to compare
// (Connection.pm server_key). It reads a config as Clean writes it — Port a
// number, Ssl/SASL booleans — and as it's decoded from the database (numbers
// as json.Number). A value of any other type counts as a change, so the
// connection reconnects. The nick is one: liercd registers with it, so
// changing it reconnects.
func serverKey(c map[string]any) string {
	text := func(v any) string {
		if s, ok := v.(string); ok {
			return s
		}
		return ""
	}
	port := "\x01" // no or a malformed port: never equal to a real one
	switch p := c["Port"].(type) {
	case json.Number:
		port = p.String()
	case int:
		port = strconv.Itoa(p)
	}
	flag := func(v any) string {
		switch v {
		case true:
			return "1"
		case false, nil:
			return "0"
		}
		return "\x01" // not a boolean: never equal to one
	}
	return strings.Join([]string{
		strings.TrimSpace(strings.ToLower(text(c["Host"]))),
		port, text(c["Nick"]), text(c["User"]), text(c["Pass"]),
		flag(c["Ssl"]), flag(c["SASL"]),
	}, "\x00")
}
