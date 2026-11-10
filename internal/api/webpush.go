package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/lierc/lierc-api/internal/respond"
)

func (a *api) webPush(mux *http.ServeMux) {
	mux.Handle("POST /notification/web_push", a.users.Require(a.subscribe))
	mux.Handle("DELETE /notification/web_push", a.users.Require(a.unsubscribePush))
}

var (
	endpointURL = regexp.MustCompile(`\Ahttps://([A-Za-z0-9.-]+)(?::443)?(?:/[!-~]*)?\z`)
	// The push services browsers use. The notifier POSTs to whatever
	// endpoint is saved, so only these are accepted; it also refuses
	// private addresses when sending (lierc pkg/notify/pushclient.go).
	pushHosts = []*regexp.Regexp{
		regexp.MustCompile(`\A(?:fcm\.googleapis\.com)\z`),                  // Chrome, Edge, Opera, Samsung
		regexp.MustCompile(`\A(?:updates\.push\.services\.mozilla\.com)\z`), // Firefox
		regexp.MustCompile(`\A(?:(?:[a-z0-9-]+\.)+notify\.windows\.com)\z`), // legacy Edge
		regexp.MustCompile(`\A(?:(?:[a-z0-9-]+\.)*push\.apple\.com)\z`),     // Safari
	}
	base64url = regexp.MustCompile(`\A[A-Za-z0-9_-]+={0,2}\z`)
)

// checkSubscription is why a subscription can't be saved, or "" (WebPush.pm
// check_subscription). The endpoint must be https on a known push service;
// key and auth must be the browser's P-256 public key (65 bytes,
// uncompressed) and 16 byte auth secret, base64url encoded.
func checkSubscription(endpoint, key, auth string) string {
	if endpoint == "" {
		return "endpoint is required"
	}
	if len(endpoint) > 255 {
		return "endpoint is too long"
	}
	m := endpointURL.FindStringSubmatch(endpoint)
	if m == nil {
		return "endpoint must be an https URL"
	}
	host := strings.ToLower(m[1])
	known := false
	for _, re := range pushHosts {
		known = known || re.MatchString(host)
	}
	if !known {
		return "endpoint is not a known push service"
	}
	for _, c := range []struct {
		name, value string
		size        int
	}{{"key", key, 65}, {"auth", auth, 16}} {
		if c.value == "" {
			return c.name + " is required"
		}
		if !base64url.MatchString(c.value) {
			return c.name + " is not base64url"
		}
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(c.value, "="))
		if err != nil || len(b) != c.size {
			return c.name + " has the wrong length"
		}
		if c.name == "key" && b[0] != 0x04 {
			return "key is not an uncompressed P-256 key"
		}
	}
	return ""
}

// subscribe saves the subscription in the form body (or, as with Perl's
// parameters, the query when the body lacks a field).
func (a *api) subscribe(w http.ResponseWriter, r *http.Request, user string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			respond.Error(w, http.StatusBadRequest, "request body is too large")
		} else {
			respond.Error(w, http.StatusBadRequest, "could not read the request body")
		}
		return
	}
	param := func(name string) string {
		if r.PostForm.Has(name) {
			return strings.ToValidUTF8(r.PostForm.Get(name), "�")
		}
		return strings.ToValidUTF8(r.URL.Query().Get(name), "�")
	}
	endpoint, key, auth := param("endpoint"), param("key"), param("auth")
	if msg := checkSubscription(endpoint, key, auth); msg != "" {
		respond.Error(w, http.StatusBadRequest, msg)
		return
	}
	a.done(w, "save web push", a.db.SaveWebPush(r.Context(), user, endpoint, key, auth))
}

// unsubscribePush forgets one subscription. The endpoint is in the query,
// not the path: nginx merges the slashes in a path, turning "https://"
// into "https:/".
func (a *api) unsubscribePush(w http.ResponseWriter, r *http.Request, user string) {
	endpoint := r.URL.Query().Get("endpoint")
	if endpoint == "" {
		respond.Error(w, http.StatusBadRequest, "endpoint is required")
		return
	}
	a.done(w, "delete web push", a.db.DeleteWebPush(r.Context(), user, endpoint))
}
