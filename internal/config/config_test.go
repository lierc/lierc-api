package config

import (
	"reflect"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"DBName":         {c.DBName, "lierc"},
		"AdminListen":    {c.AdminListen, "127.0.0.1:5009"},
		"Listen":         {c.Listen, ":5003"},
		"LiercdHost":     {c.LiercdHost, "127.0.0.1"},
		"LiercdNextHost": {c.LiercdNextHost, ""},
		"DBHost":         {c.DBHost, "127.0.0.1"},
	}
	for k, v := range want {
		if v[0] != v[1] {
			t.Errorf("%s = %q, want %q", k, v[0], v[1])
		}
	}
	if !reflect.DeepEqual(c.TrustedProxies, []string{"127.0.0.1", "::1"}) {
		t.Errorf("TrustedProxies = %v", c.TrustedProxies)
	}
	if c.DBMaxConns != 20 {
		t.Errorf("DBMaxConns = %d", c.DBMaxConns)
	}
	if c.AccountTokenSecret != "" || c.UnsubscribeSecret != "" {
		t.Errorf("secrets have defaults: %q %q", c.AccountTokenSecret, c.UnsubscribeSecret)
	}
}

func TestOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"LISTEN": ":9", "TRUSTED_PROXIES": "10.0.0.1, 10.0.0.2",
		"LIERCD_HOST": "lc", "LIERCD_NEXT_HOST": "lcn", "POSTGRES_HOST": "pg", "POSTGRES_USER": "u",
		"POSTGRES_PASSWORD": "p", "ADMIN_LISTEN": "127.0.0.1:7", "ACCOUNT_TOKEN_SECRET": "s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9" || c.LiercdHost != "lc" || c.LiercdNextHost != "lcn" ||
		c.DBHost != "pg" || c.DBUser != "u" || c.DBPass != "p" || c.AdminListen != "127.0.0.1:7" || c.AccountTokenSecret != "s" {
		t.Errorf("overrides not applied: %+v", c)
	}
	if !reflect.DeepEqual(c.TrustedProxies, []string{"10.0.0.1", "10.0.0.2"}) {
		t.Errorf("TrustedProxies = %v", c.TrustedProxies)
	}
}

// The signing secrets have no default: RequireSecrets names a missing one.
// API_SECRET is retired and doesn't stand in.
func TestSecretsRequired(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"UNSUBSCRIBE_SECRET": "u"}, "ACCOUNT_TOKEN_SECRET"},
		{map[string]string{"UNSUBSCRIBE_SECRET": "u", "API_SECRET": "old"}, "ACCOUNT_TOKEN_SECRET"},
		{map[string]string{"ACCOUNT_TOKEN_SECRET": "a"}, "UNSUBSCRIBE_SECRET"},
		{map[string]string{"ACCOUNT_TOKEN_SECRET": "a", "UNSUBSCRIBE_SECRET": ""}, "UNSUBSCRIBE_SECRET"},
	} {
		cfg, err := Load(env(c.env))
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.RequireSecrets(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want one naming %s", c.env, err, c.want)
		}
	}
	got, err := Load(env(map[string]string{"ACCOUNT_TOKEN_SECRET": "a", "UNSUBSCRIBE_SECRET": "u"}))
	if err == nil {
		err = got.RequireSecrets()
	}
	if err != nil || got.AccountTokenSecret != "a" || got.UnsubscribeSecret != "u" {
		t.Errorf("both set: %+v, %v", got, err)
	}
}

func TestPostgresDB(t *testing.T) {
	for v, want := range map[string]string{"other": "other", "": "lierc"} {
		c, err := Load(env(map[string]string{"POSTGRES_DB": v}))
		if err != nil || c.DBName != want {
			t.Errorf("POSTGRES_DB=%q: DBName = %q, %v; want %q", v, c.DBName, err, want)
		}
	}
}

func TestDBMaxConns(t *testing.T) {
	c, err := Load(env(map[string]string{"DB_MAX_CONNS": "5"}))
	if err != nil || c.DBMaxConns != 5 {
		t.Errorf("got %d, %v", c.DBMaxConns, err)
	}
	for _, bad := range []string{"0", "-2", "x", "99999999999"} {
		if _, err := Load(env(map[string]string{"DB_MAX_CONNS": bad})); err == nil {
			t.Errorf("DB_MAX_CONNS=%q accepted", bad)
		}
	}
}

func TestVAPIDPublic(t *testing.T) {
	c, err := Load(env(map[string]string{"VAPID_PUBLIC": "abc"}))
	if err != nil || c.VAPIDPublic != "abc" {
		t.Errorf("VAPIDPublic = %q, %v", c.VAPIDPublic, err)
	}
}

func TestOrigins(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want []string
	}{
		{map[string]string{"API_ORIGINS": "https://a https://b"}, []string{"https://a", "https://b"}},
		{map[string]string{"API_URL": "https://u"}, []string{"https://u"}},
		{nil, []string{}},
		{map[string]string{"API_ORIGINS": "https://a", "API_URL": "https://u"}, []string{"https://a"}},
	} {
		c2, err := Load(env(c.env))
		if err != nil || len(c2.Origins) != len(c.want) {
			t.Errorf("%v: Origins = %q, %v", c.env, c2.Origins, err)
			continue
		}
		for i := range c.want {
			if c2.Origins[i] != c.want[i] {
				t.Errorf("%v: Origins = %q", c.env, c2.Origins)
			}
		}
	}
}

// API_SECURE and LIERC_NO_SMTP are booleans as strconv.ParseBool reads
// them; unset or empty is false, and anything else stops Load.
func TestBooleans(t *testing.T) {
	get := map[string]func(Config) bool{
		"API_SECURE":    func(c Config) bool { return c.Secure },
		"LIERC_NO_SMTP": func(c Config) bool { return c.NoSMTP },
	}
	for name, field := range get {
		c, err := Load(env(nil))
		if err != nil || field(c) {
			t.Errorf("%s unset: %v, %v", name, field(c), err)
		}
		for v, want := range map[string]bool{"1": true, "true": true, "0": false, "false": false, "": false} {
			c, err := Load(env(map[string]string{name: v}))
			if err != nil || field(c) != want {
				t.Errorf("%s=%q: %v, %v", name, v, field(c), err)
			}
		}
		if _, err := Load(env(map[string]string{name: "yes"})); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=yes: error %v, want one naming %s", name, err, name)
		}
	}
}

func TestMailSettings(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.SMTPOpts != "" || c.FromAddress != "do-not-reply@relaychat.party" || c.SiteURL != "https://relaychat.party" || c.NoSMTP {
		t.Errorf("defaults: %q %q %q %v", c.SMTPOpts, c.FromAddress, c.SiteURL, c.NoSMTP)
	}
	c, err = Load(env(map[string]string{"SMTP_OPTS": "Host,h", "FROM_ADDRESS": "a@b.c", "API_URL": "https://x.test", "LIERC_NO_SMTP": "1"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.SMTPOpts != "Host,h" || c.FromAddress != "a@b.c" || c.SiteURL != "https://x.test" || !c.NoSMTP {
		t.Errorf("set: %q %q %q %v", c.SMTPOpts, c.FromAddress, c.SiteURL, c.NoSMTP)
	}
	c, _ = Load(env(map[string]string{"LIERC_NO_SMTP": "0"}))
	if c.NoSMTP {
		t.Error("LIERC_NO_SMTP=0 should be false")
	}
}

func TestDefaultNetwork(t *testing.T) {
	load := func(m map[string]string) *DefaultNetwork {
		t.Helper()
		c, err := Load(env(m))
		if err != nil {
			t.Fatal(err)
		}
		return c.DefaultNetwork
	}
	if n := load(nil); n != nil {
		t.Errorf("unset = %+v", n)
	}
	if _, err := Load(env(map[string]string{"DEFAULT_NETWORK_HOST": "h"})); err == nil || !strings.Contains(err.Error(), "DEFAULT_NETWORK_PORT") {
		t.Errorf("host without port: error %v, want one naming DEFAULT_NETWORK_PORT", err)
	}
	n := load(map[string]string{"DEFAULT_NETWORK_HOST": "h", "DEFAULT_NETWORK_PORT": "6667"})
	if n == nil || n.Host != "h" || n.Port != 6667 || n.Ssl || len(n.Channels) != 0 {
		t.Errorf("host and port only = %+v", n)
	}
	n = load(map[string]string{"DEFAULT_NETWORK_HOST": "h", "DEFAULT_NETWORK_PORT": "6697", "DEFAULT_NETWORK_SSL": "1", "DEFAULT_NETWORK_CHANNELS": " #a  #b\n"})
	if n == nil || n.Port != 6697 || !n.Ssl || !reflect.DeepEqual(n.Channels, []string{"#a", "#b"}) {
		t.Errorf("all set = %+v", n)
	}
	for v, want := range map[string]bool{"0": false, "false": false, "true": true} {
		if n = load(map[string]string{"DEFAULT_NETWORK_HOST": "h", "DEFAULT_NETWORK_PORT": "6667", "DEFAULT_NETWORK_SSL": v}); n.Ssl != want {
			t.Errorf("SSL=%q is %v", v, n.Ssl)
		}
	}
	if _, err := Load(env(map[string]string{"DEFAULT_NETWORK_HOST": "h", "DEFAULT_NETWORK_PORT": "6667", "DEFAULT_NETWORK_SSL": "yes"})); err == nil || !strings.Contains(err.Error(), "DEFAULT_NETWORK_SSL") {
		t.Errorf("SSL=yes: error %v, want one naming DEFAULT_NETWORK_SSL", err)
	}
	if n = load(map[string]string{"DEFAULT_NETWORK_HOST": "h", "DEFAULT_NETWORK_PORT": "6697"}); n.Port != 6697 {
		t.Errorf("port 6697 = %d", n.Port)
	}
	for _, port := range []string{"x", "-5", "0", "70000", " 6697 ", "66.5"} {
		if _, err := Load(env(map[string]string{"DEFAULT_NETWORK_HOST": "h", "DEFAULT_NETWORK_PORT": port})); err == nil || !strings.Contains(err.Error(), "DEFAULT_NETWORK_PORT") {
			t.Errorf("port %q: error %v, want one naming DEFAULT_NETWORK_PORT", port, err)
		}
	}
}

// DefaultNetworkPort is the one reading of DEFAULT_NETWORK_PORT: a whole
// number from 1 to 65535; unset is an error.
func TestDefaultNetworkPort(t *testing.T) {
	for in, want := range map[string]int{"6697": 6697, "1": 1, "65535": 65535} {
		if got, err := DefaultNetworkPort(in); err != nil || got != want {
			t.Errorf("DefaultNetworkPort(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "abc", "6697x", "70000", "-1", "66.5", " 6697 ", "+6697"} {
		if _, err := DefaultNetworkPort(in); err == nil {
			t.Errorf("DefaultNetworkPort(%q) has no error", in)
		}
	}
}

// LoadDB reads the database settings as Load does, and nothing else: an
// API setting Load would refuse doesn't matter to it.
func TestLoadDB(t *testing.T) {
	e := env(map[string]string{"POSTGRES_HOST": "db:5433", "POSTGRES_USER": "u", "POSTGRES_PASSWORD": "p",
		"POSTGRES_DB": "d", "DB_MAX_CONNS": "5", "API_SECURE": "maybe"})
	c, err := LoadDB(e)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{DBHost: "db:5433", DBUser: "u", DBPass: "p", DBName: "d", DBMaxConns: 5}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("LoadDB = %+v, want %+v", c, want)
	}

	c, err = LoadDB(env(nil))
	if err != nil || c.DBHost != "127.0.0.1" || c.DBName != "lierc" || c.DBMaxConns != DefaultDBMaxConns {
		t.Errorf("defaults: %+v %v", c, err)
	}
	if _, err := LoadDB(env(map[string]string{"DB_MAX_CONNS": "0"})); err == nil {
		t.Error("DB_MAX_CONNS=0 accepted")
	}
}
