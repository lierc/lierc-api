// Package config loads service settings from the environment, mirroring
// lib/API/Config.pm.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Config struct {
	Base, LiercdHost, DBHost, DBUser, DBPass, DBName, Secret, Key string
	Secure                                                        bool
	NSQAddr                                                       string
	SMTP                                                          SMTPOpts
	FromAddress, VapidPublic                                      string
	NoSMTP                                                        bool
	APN                                                           APNConfig
	Listen                                                        string
	TrustedProxies                                                []string
	ImgurBaseURL                                                  string
	// MaxBodyBytes caps a request body (MAX_BODY_BYTES); larger is a 413.
	MaxBodyBytes int64
	// DBMaxConns is the Postgres pool size (DB_MAX_CONNS).
	DBMaxConns int
}

// DefaultDBMaxConns is the DB_MAX_CONNS default.
const DefaultDBMaxConns = 20

// DefaultMaxBodyBytes is the MAX_BODY_BYTES default, 32 MiB.
const DefaultMaxBodyBytes = 32 << 20

type SMTPOpts struct {
	Host     string
	Port     int
	SSL      bool
	Hello    string
	AuthUser string
	AuthPass string
	Extra    map[string]string
}

type APNConfig struct {
	WebsiteName, WebsitePushID string
	AllowedDomains             []string
	FormatString, ServiceURL   string
	CertFile, KeyFile          string
}

// Load builds a Config. mode is "api" or "stream". As in Config.pm, an empty
// variable counts as unset (Perl's `||`).
func Load(getenv func(string) string, mode string) (Config, error) {
	or := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}

	c := Config{
		Base:        or("API_BASE", ""),
		LiercdHost:  or("LIERCD_HOST", "127.0.0.1"),
		DBHost:      or("POSTGRES_HOST", "127.0.0.1"),
		DBUser:      or("POSTGRES_USER", ""),
		DBPass:      or("POSTGRES_PASSWORD", ""),
		DBName:      or("POSTGRES_DB", "lierc"),
		Secret:      or("API_SECRET", "changeme"),
		Key:         or("API_KEY", "changeme"),
		Secure:      perlTrue(getenv("API_SECURE")),
		NSQAddr:     or("NSQD_HOST", "127.0.0.1") + ":4150",
		FromAddress: or("FROM_ADDRESS", "do-not-reply@relaychat.party"),
		VapidPublic: getenv("VAPID_PUBLIC"),
		NoSMTP:      getenv("LIERC_NO_SMTP") != "",
		// Test-only override; not documented for production.
		ImgurBaseURL: or("LIERC_IMGUR_URL", "https://api.imgur.com"),
		APN: APNConfig{
			WebsiteName:    or("APN_NAME", "Relaychat Party"),
			WebsitePushID:  or("APN_PUSHID", "web.party.relaychat"),
			AllowedDomains: strings.Fields(or("APN_ALLOWED_DOMAINS", "https://relaychat.party")),
			FormatString:   or("APN_FORMAT_STRING", "https://relaychat.party/app/#!/%@/%@"),
			ServiceURL:     or("APN_SERVICE_URL", "https://relaychat.party"),
			CertFile:       or("APN_CERT_FILE", ".apn/apn.pem"),
			KeyFile:        or("APN_KEY_FILE", ".apn/apn.key"),
		},
	}

	switch mode {
	case "api":
		c.Listen = or("LISTEN", ":5004")
	case "stream":
		// Stream's Postgres pool hard-codes dbname "lierc" (bin/stream.psgi).
		c.DBName = "lierc"
		c.Listen = or("LISTEN", ":5003")
	default:
		return Config{}, fmt.Errorf("unknown mode %q", mode)
	}

	for _, p := range strings.Split(or("TRUSTED_PROXIES", "127.0.0.1,::1"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			c.TrustedProxies = append(c.TrustedProxies, p)
		}
	}

	n, err := positiveInt(or("MAX_BODY_BYTES", ""), DefaultMaxBodyBytes)
	if err != nil {
		return Config{}, fmt.Errorf("MAX_BODY_BYTES: %w", err)
	}
	c.MaxBodyBytes = n

	n, err = positiveInt(or("DB_MAX_CONNS", ""), DefaultDBMaxConns)
	if err != nil || n > 1<<31-1 {
		return Config{}, fmt.Errorf("DB_MAX_CONNS: %q is not a positive integer", getenv("DB_MAX_CONNS"))
	}
	c.DBMaxConns = int(n)

	smtp, err := parseSMTPOpts(getenv("SMTP_OPTS"))
	if err != nil {
		return Config{}, err
	}
	c.SMTP = smtp
	return c, nil
}

// positiveInt parses s as a positive integer, or returns def when s is empty.
func positiveInt(s string, def int64) (int64, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a positive integer", s)
	}
	return n, nil
}

// perlTrue reports Perl truthiness of an env string: "" and "0" are false.
func perlTrue(s string) bool { return s != "" && s != "0" }

func parseSMTPOpts(raw string) (SMTPOpts, error) {
	if raw == "" {
		return SMTPOpts{Host: "localhost", Extra: map[string]string{}}, nil
	}
	parts := strings.Split(raw, ",")
	// Perl's split /,/ drops trailing empty fields.
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts)%2 != 0 {
		return SMTPOpts{}, errors.New("SMTP_OPTS must be a comma separated list of key,value pairs")
	}
	o := SMTPOpts{Extra: map[string]string{}}
	for i := 0; i < len(parts); i += 2 {
		k, v := parts[i], parts[i+1]
		switch k {
		case "Host":
			o.Host = v
		case "Port":
			n, err := strconv.Atoi(v)
			if err != nil {
				return SMTPOpts{}, fmt.Errorf("SMTP_OPTS Port %q is not a number", v)
			}
			o.Port = n
		case "SSL":
			o.SSL = perlTrue(v)
		case "Hello":
			o.Hello = v
		case "AuthUser":
			o.AuthUser = v
		case "AuthPass":
			o.AuthPass = v
		default:
			o.Extra[k] = v
		}
	}
	return o, nil
}
