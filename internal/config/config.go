// Package config loads the stream server's settings from the environment,
// mirroring lib/API/Config.pm.
package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

type Config struct {
	LiercdHost, DBHost, DBUser, DBPass, DBName, Secret, Key string
	NSQAddr                                                 string
	Listen                                                  string
	TrustedProxies                                          []string
	// DBMaxConns is the Postgres pool size (DB_MAX_CONNS).
	DBMaxConns int
}

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
		LiercdHost: or("LIERCD_HOST", "127.0.0.1"),
		DBHost:     or("POSTGRES_HOST", "127.0.0.1"),
		DBUser:     or("POSTGRES_USER", ""),
		DBPass:     or("POSTGRES_PASSWORD", ""),
		// Stream's Postgres pool hard-codes dbname "lierc" (bin/stream.psgi),
		// so POSTGRES_DB is ignored.
		DBName:  "lierc",
		Secret:  or("API_SECRET", "changeme"),
		Key:     or("API_KEY", "changeme"),
		NSQAddr: or("NSQD_HOST", "127.0.0.1"),
		Listen:  or("LISTEN", ":5003"),
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
