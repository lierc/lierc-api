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
	// VAPIDPublic is the Web Push public key the UI subscribes with
	// (VAPID_PUBLIC); empty when unset.
	VAPIDPublic string
	// Origins are the origins the UI is served from, which may make
	// state-changing requests: API_ORIGINS split on whitespace, else
	// API_URL (API::Config origins). Empty counts as unset.
	Origins []string
	// Secure marks the login cookie Secure (API_SECURE, true as Perl reads
	// it: set, and not "0").
	Secure bool
	// SMTPOpts is SMTP_OPTS, parsed by mail.ParseOpts; "" when unset.
	SMTPOpts string
	// FromAddress is the sender of the API's emails (FROM_ADDRESS).
	FromAddress string
	// SiteURL begins the links in those emails (API_URL).
	SiteURL string
	// NoSMTP skips sending email (LIERC_NO_SMTP, true as Perl reads it:
	// set, and not "0").
	NoSMTP bool
	// DefaultNetwork is the network new users get when they confirm their
	// email (DEFAULT_NETWORK_*); nil when DEFAULT_NETWORK_HOST is empty.
	DefaultNetwork *DefaultNetwork
}

// DefaultNetwork is the signup network (API::Config default_network).
type DefaultNetwork struct {
	Host     string
	Port     int
	Ssl      bool
	Channels []string
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
		VAPIDPublic:    getenv("VAPID_PUBLIC"),
		Origins:        origins(getenv),
		Secure:         getenv("API_SECURE") != "" && getenv("API_SECURE") != "0",
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
		SMTPOpts:    getenv("SMTP_OPTS"),
		FromAddress: or("FROM_ADDRESS", "do-not-reply@relaychat.party"),
		SiteURL:     or("API_URL", "https://relaychat.party"),
		NoSMTP:      getenv("LIERC_NO_SMTP") != "" && getenv("LIERC_NO_SMTP") != "0",
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
	if host := getenv("DEFAULT_NETWORK_HOST"); host != "" {
		n := &DefaultNetwork{
			Host:     host,
			Port:     DefaultNetworkPort(getenv("DEFAULT_NETWORK_PORT")),
			Ssl:      getenv("DEFAULT_NETWORK_SSL") != "" && getenv("DEFAULT_NETWORK_SSL") != "0",
			Channels: strings.Fields(getenv("DEFAULT_NETWORK_CHANNELS")),
		}
		c.DefaultNetwork = n
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

// DefaultNetworkPort is DEFAULT_NETWORK_PORT as a port: a whole number from
// 1 to 65535, surrounding spaces allowed, or 6667. A bad value never stops
// startup. connconfig.Exempt reads it the same way, so the default network
// is exempt on the port it's created with.
func DefaultNetworkPort(s string) int {
	if p, err := strconv.ParseInt(strings.TrimSpace(s), 10, 32); err == nil && p >= 1 && p <= 65535 {
		return int(p)
	}
	return 6667
}
