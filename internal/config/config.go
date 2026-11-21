// Package config loads the API's settings from the environment.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Config struct {
	LiercdHost, DBHost, DBUser, DBPass, DBName string
	// AccountTokenSecret signs the email verification and password reset
	// links (ACCOUNT_TOKEN_SECRET).
	AccountTokenSecret string
	// UnsubscribeSecret signs the unsubscribe links in notification
	// emails (UNSUBSCRIBE_SECRET); lierc ingest's notifier reads it too.
	UnsubscribeSecret string
	Listen            string
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
	// API_URL. Empty counts as unset.
	Origins []string
	// Secure marks the login cookie Secure (API_SECURE, a boolean).
	Secure bool
	// SMTPOpts is SMTP_OPTS, parsed by mail.ParseOpts; "" when unset.
	SMTPOpts string
	// FromAddress is the sender of the API's emails (FROM_ADDRESS).
	FromAddress string
	// SiteURL begins the links in those emails (API_URL).
	SiteURL string
	// NoSMTP skips sending email (LIERC_NO_SMTP, a boolean).
	NoSMTP bool
	// DefaultNetwork is the network new users get when they confirm their
	// email (DEFAULT_NETWORK_*); nil when DEFAULT_NETWORK_HOST is empty.
	DefaultNetwork *DefaultNetwork
}

// DefaultNetwork is the signup network.
type DefaultNetwork struct {
	Host     string
	Port     int
	Ssl      bool
	Channels []string
}

// DefaultDBMaxConns is the DB_MAX_CONNS default.
const DefaultDBMaxConns = 20

// orElse is getenv's value for key, or def when that's empty.
func orElse(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

// LoadDB builds a Config with only the database settings, read as Load
// reads them (POSTGRES_*, DB_MAX_CONNS), for lierc ingest, which has none
// of the API's other settings. An empty variable counts as unset.
func LoadDB(getenv func(string) string) (Config, error) {
	c := Config{
		DBHost: orElse(getenv, "POSTGRES_HOST", "127.0.0.1"),
		DBUser: getenv("POSTGRES_USER"),
		DBPass: getenv("POSTGRES_PASSWORD"),
		DBName: orElse(getenv, "POSTGRES_DB", "lierc"),
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

// Load builds a Config. An empty variable counts as unset.
func Load(getenv func(string) string) (Config, error) {
	or := func(key, def string) string { return orElse(getenv, key, def) }

	db, err := LoadDB(getenv)
	if err != nil {
		return Config{}, err
	}
	c := Config{
		DBHost:             db.DBHost,
		DBUser:             db.DBUser,
		DBPass:             db.DBPass,
		DBName:             db.DBName,
		DBMaxConns:         db.DBMaxConns,
		VAPIDPublic:        getenv("VAPID_PUBLIC"),
		Origins:            origins(getenv),
		LiercdHost:         or("LIERCD_HOST", "127.0.0.1"),
		LiercdNextHost:     or("LIERCD_NEXT_HOST", ""),
		AccountTokenSecret: getenv("ACCOUNT_TOKEN_SECRET"),
		UnsubscribeSecret:  getenv("UNSUBSCRIBE_SECRET"),
		Listen:             or("LISTEN", ":5003"),
		AdminListen:        or("ADMIN_LISTEN", "127.0.0.1:5009"),
		SMTPOpts:           getenv("SMTP_OPTS"),
		FromAddress:        or("FROM_ADDRESS", "do-not-reply@relaychat.party"),
		SiteURL:            or("API_URL", "https://relaychat.party"),
	}

	for _, p := range strings.Split(or("TRUSTED_PROXIES", "127.0.0.1,::1"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			c.TrustedProxies = append(c.TrustedProxies, p)
		}
	}

	if host := getenv("DEFAULT_NETWORK_HOST"); host != "" {
		n := &DefaultNetwork{
			Host:     host,
			Channels: strings.Fields(getenv("DEFAULT_NETWORK_CHANNELS")),
		}
		if n.Port, err = DefaultNetworkPort(getenv("DEFAULT_NETWORK_PORT")); err != nil {
			return Config{}, err
		}
		if n.Ssl, err = boolean(getenv, "DEFAULT_NETWORK_SSL"); err != nil {
			return Config{}, err
		}
		c.DefaultNetwork = n
	}
	if c.Secure, err = boolean(getenv, "API_SECURE"); err != nil {
		return Config{}, err
	}
	if c.NoSMTP, err = boolean(getenv, "LIERC_NO_SMTP"); err != nil {
		return Config{}, err
	}
	return c, nil
}

// boolean reads name as strconv.ParseBool does; unset or empty is false.
func boolean(getenv func(string) string, name string) (bool, error) {
	v := getenv(name)
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %q is not true or false", name, v)
	}
	return b, nil
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
// 1 to 65535. Anything else, unset included, is an error, and Load fails
// when DEFAULT_NETWORK_HOST is set. connconfig.Exempt reads it the same
// way, so the default network is exempt on the port it's created with.
func DefaultNetworkPort(s string) (int, error) {
	if s == "" {
		return 0, errors.New("DEFAULT_NETWORK_PORT is not set")
	}
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 || strconv.Itoa(p) != s {
		return 0, fmt.Errorf("DEFAULT_NETWORK_PORT: %q is not a port from 1 to 65535", s)
	}
	return p, nil
}

// RequireSecrets reports a missing signing secret. They have no default,
// so the API refuses to start without them rather than sign links with a
// guessable key; lctl, which signs nothing, doesn't call it.
func (c Config) RequireSecrets() error {
	for _, s := range []struct{ name, v string }{
		{"ACCOUNT_TOKEN_SECRET", c.AccountTokenSecret},
		{"UNSUBSCRIBE_SECRET", c.UnsubscribeSecret},
	} {
		if s.v == "" {
			return fmt.Errorf("%s is not set", s.name)
		}
	}
	return nil
}
