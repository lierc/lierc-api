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
		"PerlAPI":        {c.PerlAPI, "http://127.0.0.1:5004"},
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
		"PERL_API": "http://perl:5004",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9" || c.NSQAddr != "nsq:4150" || c.LiercdHost != "lc" || c.LiercdNextHost != "lcn" ||
		c.DBHost != "pg" || c.DBUser != "u" || c.DBPass != "p" || c.AdminListen != "127.0.0.1:7" || c.Secret != "s" ||
		c.PerlAPI != "http://perl:5004" {
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

func TestPerlAPIInvalid(t *testing.T) {
	for _, v := range []string{"127.0.0.1:5004", "ftp://x", "http://"} {
		if _, err := Load(env(map[string]string{"PERL_API": v})); err == nil {
			t.Errorf("PERL_API=%q: no error", v)
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
