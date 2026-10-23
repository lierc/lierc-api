package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/testenv"
	"github.com/smallstep/pkcs7"
)

type apnEnv struct {
	*connEnv
	signer, ca *x509.Certificate
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setupAPN serves the route table with AssetDir set to a temp dir holding
// copied icons and a freshly generated CA (standing in for WWDR), signer cert
// and key.
func setupAPN(t *testing.T) *apnEnv {
	t.Helper()
	d := testenv.Postgres(t)
	id, _, err := d.AddUser(context.Background(), "alice", "alice@example.com", "x")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Cfg: config.Config{Secret: authSecret}, DB: d}
	ae := &authEnv{t: t, db: d, id: id}
	ae.codec.Secret = authSecret
	ae.h = ae.codec.Middleware(newHandler(app, routes))
	e := &connEnv{authEnv: ae}
	dir := t.TempDir()
	for _, i := range apnIcons {
		name := "icon.iconset/icon_" + i + ".png"
		b, err := os.ReadFile("../../" + name)
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Join(dir, "icon.iconset"), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test WWDR"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Test Signer"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := x509.ParseCertificate(der)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	writePEM(t, filepath.Join(dir, ".apn", "AppleWWDRCA.pem"), "CERTIFICATE", caDER)
	writePEM(t, filepath.Join(dir, ".apn", "apn.pem"), "CERTIFICATE", der)
	writePEM(t, filepath.Join(dir, ".apn", "apn.key"), "EC PRIVATE KEY", keyDER)

	app.AssetDir = dir
	app.Cfg.APN = config.APNConfig{
		WebsiteName: "Test Site", WebsitePushID: "web.test", AllowedDomains: []string{"https://a.example", "https://b.example"},
		FormatString: "https://a.example/%@/%@", ServiceURL: "https://a.example",
		CertFile: ".apn/apn.pem", KeyFile: ".apn/apn.key",
	}
	return &apnEnv{connEnv: e, signer: signer, ca: ca}
}

func (e *apnEnv) anon(method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *apnEnv) devices(user string) []string {
	rows, err := e.db.Pool.Query(context.Background(), `SELECT device_id FROM apn WHERE "user"=$1 ORDER BY device_id`, user)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		rows.Scan(&d)
		out = append(out, d)
	}
	return out
}

func TestAPNConfig(t *testing.T) {
	e := setupAPN(t)
	rec := e.get("/notification/apn/config")
	want := `{"push_id":"web.test","service_url":"https://a.example","user":"` + e.id + `"}`
	if rec.Code != 200 || rec.Body.String() != want {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := e.anon("GET", "/notification/apn/config", ""); rec.Code != 401 {
		t.Fatalf("anon config = %d", rec.Code)
	}
}

func TestDevice(t *testing.T) {
	e := setupAPN(t)
	for i := 0; i < 2; i++ { // second call takes the conflict path
		rec := e.raw("POST", "/notification/apn/device", "tok123")
		if rec.Code != 200 || rec.Body.String() != `{"status":"ok"}` {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	if got := e.devices(e.id); len(got) != 1 || got[0] != "tok123" {
		t.Fatalf("devices = %v", got)
	}
}

func TestRegisterUnregister(t *testing.T) {
	e := setupAPN(t)
	reg := "/v1/devices/dev1/registrations/web.test"
	rec := e.anon("POST", reg, "", "Authorization", "  ApplePushNotifications \t "+e.id)
	if rec.Code != 200 || rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	if got := e.devices(e.id); len(got) != 1 || got[0] != "dev1" {
		t.Fatalf("devices = %v", got)
	}
	rec = e.anon("DELETE", reg, "", "Authorization", "Basic "+e.id)
	if rec.Code != 400 || rec.Body.String() != `{"status":"error","error":"Invalid authorization header"}` {
		t.Fatalf("bad scheme: %d %s", rec.Code, rec.Body)
	}
	if rec := e.anon("POST", reg, ""); rec.Code != 400 {
		t.Fatalf("no header: %d", rec.Code)
	}
	if len(e.devices(e.id)) != 1 {
		t.Fatal("bad request changed devices")
	}
	rec = e.anon("DELETE", reg, "", "Authorization", "ApplePushNotifications "+e.id)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("unregister: %d %s", rec.Code, rec.Body)
	}
	if got := e.devices(e.id); len(got) != 0 {
		t.Fatalf("devices = %v", got)
	}
}

func TestLog(t *testing.T) {
	e := setupAPN(t)
	if rec := e.anon("POST", "/v1/log", `{"logs":["x"]}`); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestPackage(t *testing.T) {
	e := setupAPN(t)
	rec := e.anon("POST", "/v1/pushPackages/web.test", `{"user":"tok-alice"}`)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if rec.Header().Get("Content-Lenght") == "" {
		t.Error("missing Content-Lenght")
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		files[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	want := []string{"website.json", "manifest.json", "signature"}
	for _, i := range apnIcons {
		want = append(want, "icon.iconset/icon_"+i+".png")
	}
	if len(files) != len(want) {
		t.Fatalf("zip has %d files, want %d", len(files), len(want))
	}
	for _, n := range want {
		if _, ok := files[n]; !ok {
			t.Fatalf("zip lacks %s", n)
		}
	}

	var site map[string]any
	if err := json.Unmarshal(files["website.json"], &site); err != nil {
		t.Fatal(err)
	}
	wantSite := `{"allowedDomains":["https://a.example","https://b.example"],"authenticationToken":"tok-alice","urlFormatString":"https://a.example/%@/%@","webServiceURL":"https://a.example","websiteName":"Test Site","websitePushID":"web.test"}`
	got, _ := json.Marshal(site)
	if string(got) != wantSite {
		t.Fatalf("website.json = %s", got)
	}

	var manifest map[string]string
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest) != len(want)-2 {
		t.Fatalf("manifest = %v", manifest)
	}
	for n, sum := range manifest {
		h := sha1.Sum(files[n])
		if hex.EncodeToString(h[:]) != sum {
			t.Errorf("manifest[%s] = %s", n, sum)
		}
	}

	p7, err := pkcs7.Parse(files["signature"])
	if err != nil {
		t.Fatal(err)
	}
	if len(p7.Content) != 0 {
		t.Error("signature is not detached")
	}
	p7.Content = files["manifest.json"]
	if err := p7.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(e.ca)
	if err := p7.VerifyWithChain(pool); err != nil {
		t.Fatalf("verify chain: %v", err)
	}
	var hasSigner, hasCA bool
	for _, c := range p7.Certificates {
		hasSigner = hasSigner || c.Equal(e.signer)
		hasCA = hasCA || c.Equal(e.ca)
	}
	if !hasSigner || !hasCA {
		t.Errorf("certs: signer=%v ca=%v", hasSigner, hasCA)
	}
	p7.Content = []byte("tampered")
	if p7.Verify() == nil {
		t.Error("tampered content verified")
	}
}

func TestPackageBadJSON(t *testing.T) {
	e := setupAPN(t)
	if rec := e.anon("POST", "/v1/pushPackages/web.test", `not json`); rec.Code != 400 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
