package config

import (
	"reflect"
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
		"Secret":         {c.Secret, "changeme"},
		"AdminListen":    {c.AdminListen, "127.0.0.1:5009"},
		"NSQAddr":        {c.NSQAddr, "127.0.0.1:4150"},
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
}

func TestOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"LISTEN": ":9", "TRUSTED_PROXIES": "10.0.0.1, 10.0.0.2", "NSQD_HOST": "nsq",
		"LIERCD_HOST": "lc", "LIERCD_NEXT_HOST": "lcn", "POSTGRES_HOST": "pg", "POSTGRES_USER": "u",
		"POSTGRES_PASSWORD": "p", "ADMIN_LISTEN": "127.0.0.1:7", "API_SECRET": "s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9" || c.NSQAddr != "nsq:4150" || c.LiercdHost != "lc" || c.LiercdNextHost != "lcn" ||
		c.DBHost != "pg" || c.DBUser != "u" || c.DBPass != "p" || c.AdminListen != "127.0.0.1:7" || c.Secret != "s" {
		t.Errorf("overrides not applied: %+v", c)
	}
	if !reflect.DeepEqual(c.TrustedProxies, []string{"10.0.0.1", "10.0.0.2"}) {
		t.Errorf("TrustedProxies = %v", c.TrustedProxies)
	}
}

func TestNSQDHostWithPort(t *testing.T) {
	for in, want := range map[string]string{"nsq:4999": "nsq:4999", "::1": "[::1]:4150", "[::1]:5": "[::1]:5"} {
		c, _ := Load(env(map[string]string{"NSQD_HOST": in}))
		if c.NSQAddr != want {
			t.Errorf("NSQD_HOST=%q: NSQAddr = %q, want %q", in, c.NSQAddr, want)
		}
	}
}

func TestIgnoresPostgresDB(t *testing.T) {
	c, _ := Load(env(map[string]string{"POSTGRES_DB": "other"}))
	if c.DBName != "lierc" {
		t.Errorf("DBName = %q", c.DBName)
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

func TestSecure(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "yes": true, "0": false, "": false} {
		c, err := Load(env(map[string]string{"API_SECURE": v}))
		if err != nil || c.Secure != want {
			t.Errorf("API_SECURE=%q: %v, %v", v, c.Secure, err)
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
	n := load(map[string]string{"DEFAULT_NETWORK_HOST": "h"})
	if n == nil || n.Host != "h" || n.Port != 6667 || n.Ssl || len(n.Channels) != 0 {
		t.Errorf("host only = %+v", n)
	}
	n = load(map[string]string{"DEFAULT_NETWORK_HOST": "h", "DEFAULT_NETWORK_PORT": "6697", "DEFAULT_NETWORK_SSL": "1", "DEFAULT_NETWORK_CHANNELS": " #a  #b\n"})
	if n == nil || n.Port != 6697 || !n.Ssl || !reflect.DeepEqual(n.Channels, []string{"#a", "#b"}) {
		t.Errorf("all set = %+v", n)
	}
	if n = load(map[string]string{"DEFAULT_NETWORK_HOST": "h", "DEFAULT_NETWORK_SSL": "0"}); n.Ssl {
		t.Error("SSL=0 is true")
	}
	for port, want := range map[string]int{"x": 6667, "-5": 6667, "0": 6667, "70000": 6667, " 6697 ": 6697, "6697": 6697} {
		if n = load(map[string]string{"DEFAULT_NETWORK_HOST": "h", "DEFAULT_NETWORK_PORT": port}); n.Port != want {
			t.Errorf("port %q = %d, want %d", port, n.Port, want)
		}
	}
}

// DefaultNetworkPort is the one reading of DEFAULT_NETWORK_PORT: a whole
// number from 1 to 65535 (surrounding spaces allowed), or 6667.
func TestDefaultNetworkPort(t *testing.T) {
	for in, want := range map[string]int{
		"": 6667, "0": 6667, "abc": 6667, "6697x": 6667, "70000": 6667, "-1": 6667, "66.5": 6667,
		"6697": 6697, " 6697 ": 6697, "1": 1, "65535": 65535,
	} {
		if got := DefaultNetworkPort(in); got != want {
			t.Errorf("DefaultNetworkPort(%q) = %d, want %d", in, got, want)
		}
	}
}
