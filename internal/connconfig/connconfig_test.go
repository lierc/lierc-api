package connconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The cases are shared with t/connection_config.t; expected values are what
// lib/API/ConnectionConfig.pm returns.
type cases struct {
	Clean []struct {
		Name  string          `json:"name"`
		Input string          `json:"input"`
		Want  json.RawMessage `json:"want"`
		Error *string         `json:"error"`
		// Go's own result where it's stricter than Perl (JSON strings
		// required in text fields)
		Go *struct {
			Want  json.RawMessage `json:"want"`
			Error *string         `json:"error"`
		} `json:"go"`
	} `json:"clean"`
	Blocked []struct {
		IP      string `json:"ip"`
		Blocked bool   `json:"blocked"`
	} `json:"blocked"`
	Exempt []struct {
		EnvHost *string `json:"env_host"`
		EnvPort *string `json:"env_port"`
		Host    string  `json:"host"`
		Port    int     `json:"port"`
		Exempt  bool    `json:"exempt"`
	} `json:"exempt"`
	CheckHost []struct {
		EnvHost  *string  `json:"env_host"`
		EnvPort  *string  `json:"env_port"`
		Host     string   `json:"host"`
		Port     int      `json:"port"`
		Resolves []string `json:"resolves"`
		Error    string   `json:"error"`
		Resolved bool     `json:"resolved"`
	} `json:"check_host"`
}

func load(t *testing.T) cases {
	t.Helper()
	b, err := os.ReadFile("../../testdata/connection-config-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var c cases
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Clean) == 0 || len(c.Blocked) == 0 || len(c.Exempt) == 0 || len(c.CheckHost) == 0 {
		t.Fatal("case file has an empty section")
	}
	return c
}

func env(host, port *string) func(string) string {
	return func(k string) string {
		switch {
		case k == "DEFAULT_NETWORK_HOST" && host != nil:
			return *host
		case k == "DEFAULT_NETWORK_PORT" && port != nil:
			return *port
		}
		return ""
	}
}

func normal(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSharedCleanCases(t *testing.T) {
	for _, c := range load(t).Clean {
		if c.Go != nil {
			c.Want, c.Error = c.Go.Want, c.Go.Error
		}
		t.Run(c.Name, func(t *testing.T) {
			got, err := Clean([]byte(c.Input))
			if c.Error != nil {
				if err == nil {
					t.Fatalf("accepted as %s, want error %q", normal(t, got), *c.Error)
				}
				if err.Error() != *c.Error {
					t.Fatalf("error %q, want %q", err.Error(), *c.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("rejected: %v", err)
			}
			var want any
			json.Unmarshal(c.Want, &want)
			if g := normal(t, got); !reflect.DeepEqual(g, want) {
				t.Fatalf("got %v\nwant %v", g, want)
			}
		})
	}
}

func TestSharedBlockedCases(t *testing.T) {
	for _, c := range load(t).Blocked {
		if got := BlockedIP(c.IP); got != c.Blocked {
			t.Errorf("BlockedIP(%q) = %v, want %v", c.IP, got, c.Blocked)
		}
	}
}

func TestSharedExemptCases(t *testing.T) {
	for _, c := range load(t).Exempt {
		if got := Exempt(env(c.EnvHost, c.EnvPort), c.Host, c.Port); got != c.Exempt {
			t.Errorf("Exempt(%q:%d) with %v:%v = %v, want %v", c.Host, c.Port, deref(c.EnvHost), deref(c.EnvPort), got, c.Exempt)
		}
	}
}

func TestSharedCheckHostCases(t *testing.T) {
	for _, c := range load(t).CheckHost {
		name := fmt.Sprintf("%s:%d->%s", c.Host, c.Port, strings.Join(c.Resolves, ","))
		t.Run(name, func(t *testing.T) {
			called := false
			resolve := func(ctx context.Context, host string) ([]string, error) {
				called = true
				if host != c.Host {
					t.Errorf("resolved %q, want %q", host, c.Host)
				}
				return c.Resolves, nil
			}
			err := CheckHost(context.Background(), env(c.EnvHost, c.EnvPort), resolve, c.Host, c.Port)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != c.Error {
				t.Errorf("error %q, want %q", got, c.Error)
			}
			if called != c.Resolved {
				t.Errorf("resolver called = %v, want %v", called, c.Resolved)
			}
		})
	}
}

func TestResolverErrorIsUnresolved(t *testing.T) {
	resolve := func(context.Context, string) ([]string, error) { return nil, fmt.Errorf("no such host") }
	err := CheckHost(context.Background(), env(nil, nil), resolve, "a.com", 6667)
	if err == nil || err.Error() != "Couldn't find host a.com" {
		t.Fatalf("got %v", err)
	}
}

func TestSystemResolverLocalhost(t *testing.T) {
	err := CheckHost(context.Background(), env(nil, nil), SystemResolver, "localhost", 6667)
	if err == nil || err.Error() != "Host localhost points to a private address" {
		t.Fatalf("got %v", err)
	}
}

func deref(s *string) string {
	if s == nil {
		return "unset"
	}
	return *s
}

// A JSON number in a text field is refused like a boolean, including ones
// the shared cases leave out (Perl saves 1e-400 as the number 0).
func TestNumbersInTextFieldsRefused(t *testing.T) {
	for _, f := range []string{"User", "Alias"} {
		if _, err := Clean([]byte(`{"Host":"irc.libera.chat","Port":6697,"Nick":"lee","` + f + `":1e-400}`)); err == nil {
			t.Errorf("%s 1e-400 accepted", f)
		}
	}
	if _, err := Clean([]byte(`{"Host":"irc.libera.chat","Port":"6697","Nick":"lee"}`)); err != nil {
		t.Errorf("Port as a string refused: %v", err)
	}
}
