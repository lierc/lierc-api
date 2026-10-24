package config

import (
	"reflect"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := Load(env(nil), "api")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"DBName":       {c.DBName, "lierc"},
		"Secret":       {c.Secret, "changeme"},
		"Key":          {c.Key, "changeme"},
		"NSQAddr":      {c.NSQAddr, "127.0.0.1:4150"},
		"Listen":       {c.Listen, ":5004"},
		"LiercdHost":   {c.LiercdHost, "127.0.0.1"},
		"DBHost":       {c.DBHost, "127.0.0.1"},
		"SMTP.Host":    {c.SMTP.Host, "localhost"},
		"FromAddress":  {c.FromAddress, "do-not-reply@relaychat.party"},
		"ImgurBaseURL": {c.ImgurBaseURL, "https://api.imgur.com"},
		"APN.Name":     {c.APN.WebsiteName, "Relaychat Party"},
		"APN.PushID":   {c.APN.WebsitePushID, "web.party.relaychat"},
		"APN.Format":   {c.APN.FormatString, "https://relaychat.party/app/#!/%@/%@"},
		"APN.Service":  {c.APN.ServiceURL, "https://relaychat.party"},
		"APN.Cert":     {c.APN.CertFile, ".apn/apn.pem"},
		"APN.Key":      {c.APN.KeyFile, ".apn/apn.key"},
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
	if c.MaxBodyBytes != 33554432 {
		t.Errorf("MaxBodyBytes = %d", c.MaxBodyBytes)
	}
	if !reflect.DeepEqual(c.APN.AllowedDomains, []string{"https://relaychat.party"}) {
		t.Errorf("AllowedDomains = %v", c.APN.AllowedDomains)
	}
	if c.Secure || c.NoSMTP || c.Base != "" {
		t.Errorf("unexpected Secure/NoSMTP/Base: %+v", c)
	}
	s, _ := Load(env(nil), "stream")
	if s.Listen != ":5003" {
		t.Errorf("stream Listen = %q", s.Listen)
	}
}

func TestOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"LISTEN": ":9", "TRUSTED_PROXIES": "10.0.0.1,10.0.0.2", "NSQD_HOST": "nsq",
		"LIERC_IMGUR_URL": "http://stub", "LIERC_NO_SMTP": "1", "VAPID_PUBLIC": "vp",
		"API_BASE": "/b", "LIERCD_HOST": "lc", "POSTGRES_HOST": "pg",
		"POSTGRES_USER": "u", "POSTGRES_PASSWORD": "p", "API_KEY": "k", "API_SECRET": "s",
	}), "api")
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9" || c.NSQAddr != "nsq:4150" || c.ImgurBaseURL != "http://stub" ||
		!c.NoSMTP || c.VapidPublic != "vp" || c.Base != "/b" || c.LiercdHost != "lc" ||
		c.DBHost != "pg" || c.DBUser != "u" || c.DBPass != "p" || c.Key != "k" || c.Secret != "s" {
		t.Errorf("overrides not applied: %+v", c)
	}
	if !reflect.DeepEqual(c.TrustedProxies, []string{"10.0.0.1", "10.0.0.2"}) {
		t.Errorf("TrustedProxies = %v", c.TrustedProxies)
	}
}

func TestStreamIgnoresPostgresDB(t *testing.T) {
	e := env(map[string]string{"POSTGRES_DB": "other"})
	a, _ := Load(e, "api")
	s, _ := Load(e, "stream")
	if a.DBName != "other" || s.DBName != "lierc" {
		t.Errorf("api=%q stream=%q", a.DBName, s.DBName)
	}
}

func TestSMTPOpts(t *testing.T) {
	c, err := Load(env(map[string]string{"SMTP_OPTS": "Host,smtp.x,Port,2465,SSL,1,AuthUser,u,AuthPass,p,Hello,h"}), "api")
	if err != nil {
		t.Fatal(err)
	}
	want := SMTPOpts{Host: "smtp.x", Port: 2465, SSL: true, Hello: "h", AuthUser: "u", AuthPass: "p", Extra: map[string]string{}}
	if !reflect.DeepEqual(c.SMTP, want) {
		t.Errorf("got %+v", c.SMTP)
	}

	c, err = Load(env(map[string]string{"SMTP_OPTS": "smtp,smtp.resend.com,SSL,1"}), "api")
	if err != nil {
		t.Fatal(err)
	}
	if c.SMTP.Host != "" || !c.SMTP.SSL || !reflect.DeepEqual(c.SMTP.Extra, map[string]string{"smtp": "smtp.resend.com"}) {
		t.Errorf("got %+v", c.SMTP)
	}

	_, err = Load(env(map[string]string{"SMTP_OPTS": "Host,a,Port"}), "api")
	if err == nil || err.Error() != "SMTP_OPTS must be a comma separated list of key,value pairs" {
		t.Errorf("err = %v", err)
	}
}

func TestSecureTruthy(t *testing.T) {
	for v, want := range map[string]bool{"": false, "0": false, "1": true, "yes": true} {
		c, _ := Load(env(map[string]string{"API_SECURE": v}), "api")
		if c.Secure != want {
			t.Errorf("API_SECURE=%q: Secure=%v want %v", v, c.Secure, want)
		}
	}
}

func TestAPNAllowedDomainsSplitOnSpace(t *testing.T) {
	c, _ := Load(env(map[string]string{"APN_ALLOWED_DOMAINS": "https://a.example  https://b.example"}), "api")
	if !reflect.DeepEqual(c.APN.AllowedDomains, []string{"https://a.example", "https://b.example"}) {
		t.Errorf("got %v", c.APN.AllowedDomains)
	}
}

func TestMaxBodyBytes(t *testing.T) {
	c, err := Load(env(map[string]string{"MAX_BODY_BYTES": "1024"}), "api")
	if err != nil || c.MaxBodyBytes != 1024 {
		t.Errorf("got %d, %v", c.MaxBodyBytes, err)
	}
	for _, bad := range []string{"0", "-1", "lots"} {
		if _, err := Load(env(map[string]string{"MAX_BODY_BYTES": bad}), "api"); err == nil {
			t.Errorf("MAX_BODY_BYTES=%q accepted", bad)
		}
	}
}

func TestDBMaxConns(t *testing.T) {
	c, err := Load(env(map[string]string{"DB_MAX_CONNS": "5"}), "stream")
	if err != nil || c.DBMaxConns != 5 {
		t.Errorf("got %d, %v", c.DBMaxConns, err)
	}
	for _, bad := range []string{"0", "-2", "x", "99999999999"} {
		if _, err := Load(env(map[string]string{"DB_MAX_CONNS": bad}), "api"); err == nil {
			t.Errorf("DB_MAX_CONNS=%q accepted", bad)
		}
	}
}
