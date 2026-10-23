package api

// Handlers ported from lib/API/Controller/APN.pm.

import (
	"archive/zip"
	"bytes"
	"crypto"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/smallstep/pkcs7"
)

var apnIcons = []string{"16x16", "16x16@2x", "32x32", "32x32@2x", "128x128", "128x128@2x"}

// apnUser parses "Authorization: ApplePushNotifications <user>" the way
// Perl's `split " "` does: leading whitespace ignored, any whitespace run
// separates fields. A missing user is nil (undef, which fails the NOT NULL).
func apnUser(r *Request) (*string, error) {
	f := strings.Fields(r.Header.Get("Authorization"))
	if len(f) == 0 || f[0] != "ApplePushNotifications" {
		return nil, errors.New("Invalid authorization header")
	}
	if len(f) < 2 {
		return nil, nil
	}
	return &f[1], nil
}

const apnUpsert = `
	INSERT INTO apn (device_id, "user")
	  VALUES ($1,$2)
	ON CONFLICT ("user", device_id)
	  DO UPDATE SET updated=NOW()`

func apnUnregister(a *App, r *Request) (Response, error) {
	user, err := apnUser(r)
	if err != nil {
		return nil, err
	}
	_, err = a.DB.Pool.Exec(r.Context(), `
		DELETE FROM apn
		WHERE "user"=$1 AND device_id=$2`, user, r.Params["device_id"])
	if err != nil {
		return nil, err
	}
	return NoContent(), nil
}

func apnDevice(a *App, r *Request) (Response, error) {
	if _, err := a.DB.Pool.Exec(r.Context(), apnUpsert, string(r.Body), r.Session.User()); err != nil {
		return nil, err
	}
	return OK(), nil
}

func apnRegister(a *App, r *Request) (Response, error) {
	user, err := apnUser(r)
	if err != nil {
		return nil, err
	}
	if _, err := a.DB.Pool.Exec(r.Context(), apnUpsert, r.Params["device_id"], user); err != nil {
		return nil, err
	}
	return OK(), nil
}

func apnLog(a *App, r *Request) (Response, error) {
	// Perl: warn $req->content
	fmt.Fprintln(os.Stderr, string(r.Body))
	return NoContent(), nil
}

func apnConfig(a *App, r *Request) (Response, error) {
	return JSON(map[string]string{
		"push_id":     a.Cfg.APN.WebsitePushID,
		"service_url": a.Cfg.APN.ServiceURL,
		"user":        r.Session.User(),
	}), nil
}

type zipResponse struct{ body []byte }

func (z zipResponse) Write(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	// Perl APN.pm misspells the header name; keep it.
	h["Content-Lenght"] = []string{strconv.Itoa(len(z.body))}
	h.Set("Content-Length", strconv.Itoa(len(z.body)))
	w.WriteHeader(http.StatusOK)
	w.Write(z.body)
}

func (a *App) assetPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(a.AssetDir, p)
}

func apnPackage(a *App, r *Request) (Response, error) {
	cfg := a.Cfg.APN

	var data map[string]any
	if err := json.Unmarshal(r.Body, &data); err != nil {
		return nil, err
	}

	domains := cfg.AllowedDomains
	if domains == nil {
		domains = []string{}
	}
	website, err := json.Marshal(map[string]any{
		"websiteName":         cfg.WebsiteName,
		"websitePushID":       cfg.WebsitePushID,
		"allowedDomains":      domains,
		"urlFormatString":     cfg.FormatString,
		"authenticationToken": data["user"],
		"webServiceURL":       cfg.ServiceURL,
	})
	if err != nil {
		return nil, err
	}

	files := []struct {
		name string
		body []byte
	}{{"website.json", website}}
	for _, icon := range apnIcons {
		name := "icon.iconset/icon_" + icon + ".png"
		b, err := os.ReadFile(filepath.Join(a.AssetDir, name))
		if err != nil {
			return nil, fmt.Errorf("Unable to copy icon: %w", err)
		}
		files = append(files, struct {
			name string
			body []byte
		}{name, b})
	}

	manifest := map[string]string{}
	for _, f := range files {
		sum := sha1.Sum(f.body)
		manifest[f.name] = hex.EncodeToString(sum[:])
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	files = append(files, struct {
		name string
		body []byte
	}{"manifest.json", manifestJSON})

	sig, err := a.apnSign(manifestJSON)
	if err != nil {
		return nil, fmt.Errorf("Failed to generate signature: %w", err)
	}
	files = append(files, struct {
		name string
		body []byte
	}{"signature", sig})

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("Failed to zip: %w", err)
	}
	return zipResponse{body: buf.Bytes()}, nil
}

// apnSign is `openssl smime -binary -sign -certfile .apn/AppleWWDRCA.pem
// -outform der`: a detached DER PKCS#7 signature carrying the signer and the
// WWDR certificates.
func (a *App) apnSign(content []byte) ([]byte, error) {
	certPEM, err := os.ReadFile(a.assetPath(a.Cfg.APN.CertFile))
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(a.assetPath(a.Cfg.APN.KeyFile))
	if err != nil {
		return nil, err
	}
	wwdrPEM, err := os.ReadFile(filepath.Join(a.AssetDir, ".apn", "AppleWWDRCA.pem"))
	if err != nil {
		return nil, err
	}

	signers, err := parseCerts(certPEM)
	if err != nil || len(signers) == 0 {
		return nil, fmt.Errorf("signer certificate: %v", err)
	}
	wwdr, err := parseCerts(wwdrPEM)
	if err != nil {
		return nil, err
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, err
	}

	sd, err := pkcs7.NewSignedData(content)
	if err != nil {
		return nil, err
	}
	if err := sd.AddSigner(signers[0], key, pkcs7.SignerInfoConfig{}); err != nil {
		return nil, err
	}
	for _, c := range append(signers[1:], wwdr...) {
		sd.AddCertificate(c)
	}
	sd.Detach()
	return sd.Finish()
}

func parseCerts(pemData []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for {
		var blk *pem.Block
		blk, pemData = pem.Decode(pemData)
		if blk == nil {
			return out, nil
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
}

func parseKey(pemData []byte) (crypto.PrivateKey, error) {
	for {
		var blk *pem.Block
		blk, pemData = pem.Decode(pemData)
		if blk == nil {
			return nil, errors.New("no private key found")
		}
		if k, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err == nil {
			return k, nil
		}
		if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
			return k, nil
		}
		if k, err := x509.ParseECPrivateKey(blk.Bytes); err == nil {
			return k, nil
		}
	}
}
