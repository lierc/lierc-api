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
		"DBName":     {c.DBName, "lierc"},
		"Secret":     {c.Secret, "changeme"},
		"Key":        {c.Key, "changeme"},
		"NSQAddr":    {c.NSQAddr, "127.0.0.1:4150"},
		"Listen":     {c.Listen, ":5003"},
		"LiercdHost": {c.LiercdHost, "127.0.0.1"},
		"DBHost":     {c.DBHost, "127.0.0.1"},
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
		"LIERCD_HOST": "lc", "POSTGRES_HOST": "pg", "POSTGRES_USER": "u",
		"POSTGRES_PASSWORD": "p", "API_KEY": "k", "API_SECRET": "s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9" || c.NSQAddr != "nsq:4150" || c.LiercdHost != "lc" ||
		c.DBHost != "pg" || c.DBUser != "u" || c.DBPass != "p" || c.Key != "k" || c.Secret != "s" {
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
