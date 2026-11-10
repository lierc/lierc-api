// Package config loads the stream server's settings from the environment,
// mirroring lib/API/Config.pm.
package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type Config struct {
	LiercdHost, DBHost, DBUser, DBPass, DBName, Secret string
	NSQAddr                                            string
	Listen                                             string
	// AdminListen is the internal admin listener (ADMIN_LISTEN), serving
	// /stats without authentication; keep it on loopback.
	AdminListen    string
	TrustedProxies []string
	// LiercdNextHost is the "next" liercd instance's host
	// (LIERCD_NEXT_HOST); "" when there is none.
	LiercdNextHost string
	// DBMaxConns is the Postgres pool size (DB_MAX_CONNS).
	DBMaxConns int
	// PerlAPI is the Perl API's base URL (PERL_API): every request the Go
	// server doesn't handle itself is passed there.
	PerlAPI string
	// VAPIDPublic is the Web Push public key the UI subscribes with
	// (VAPID_PUBLIC); empty when unset.
	VAPIDPublic string
	// Origins are the origins the UI is served from, which may make
	// state-changing requests: API_ORIGINS split on whitespace, else
	// API_URL (API::Config origins). Empty counts as unset.
	Origins []string
}

// PerlURL is c's Perl API URL, which Load has checked; a Config built
// without Load gets the default.
func (c Config) PerlURL() *url.URL {
	raw := c.PerlAPI
	if raw == "" {
		raw = DefaultPerlAPI
	}
	u, err := url.Parse(raw)
	if err != nil {
		u, _ = url.Parse(DefaultPerlAPI)
	}
	return u
}

// DefaultPerlAPI is the PERL_API default.
const DefaultPerlAPI = "http://127.0.0.1:5004"

// DefaultDBMaxConns is the DB_MAX_CONNS default.
const DefaultDBMaxConns = 20

// Load builds a Config. As in Config.pm, an empty variable counts as unset
// (Perl's `||`).
func Load(getenv func(string) string) (Config, error) {
	or := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}

	c := Config{
		VAPIDPublic:    getenv("VAPID_PUBLIC"),
		Origins:        origins(getenv),
		LiercdHost:     or("LIERCD_HOST", "127.0.0.1"),
		LiercdNextHost: or("LIERCD_NEXT_HOST", ""),
		DBHost:         or("POSTGRES_HOST", "127.0.0.1"),
		DBUser:         or("POSTGRES_USER", ""),
		DBPass:         or("POSTGRES_PASSWORD", ""),
		// Stream's Postgres pool hard-codes dbname "lierc" (bin/stream.psgi),
		// so POSTGRES_DB is ignored.
		DBName:      "lierc",
		Secret:      or("API_SECRET", "changeme"),
		NSQAddr:     or("NSQD_HOST", "127.0.0.1"),
		Listen:      or("LISTEN", ":5003"),
		AdminListen: or("ADMIN_LISTEN", "127.0.0.1:5009"),
		PerlAPI:     or("PERL_API", DefaultPerlAPI),
	}
	if u, err := url.Parse(c.PerlAPI); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Config{}, fmt.Errorf("PERL_API: %q is not an http(s) URL", c.PerlAPI)
	}
	// NSQD_HOST may carry a port; without one nsqd's default 4150 is used.
	if _, _, err := net.SplitHostPort(c.NSQAddr); err != nil {
		c.NSQAddr = net.JoinHostPort(c.NSQAddr, "4150")
	}

	for _, p := range strings.Split(or("TRUSTED_PROXIES", "127.0.0.1,::1"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			c.TrustedProxies = append(c.TrustedProxies, p)
		}
	}

	if s := getenv("DB_MAX_CONNS"); s == "" {
		c.DBMaxConns = DefaultDBMaxConns
	} else if n, err := strconv.ParseInt(s, 10, 32); err != nil || n <= 0 {
		return Config{}, fmt.Errorf("DB_MAX_CONNS: %q is not a positive integer", s)
	} else {
		c.DBMaxConns = int(n)
	}
	return c, nil
}

func origins(getenv func(string) string) []string {
	if o := strings.Fields(getenv("API_ORIGINS")); len(o) > 0 {
		return o
	}
	if u := getenv("API_URL"); u != "" {
		return []string{u}
	}
	return []string{}
}
