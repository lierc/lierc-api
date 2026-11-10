package api

import (
	"log"
	"net/http"
	"slices"

	"github.com/lierc/lierc-api/internal/respond"
)

// sameOrigin refuses requests that can change state (anything but GET,
// HEAD and OPTIONS) when the browser says they came from another site, so
// other sites' pages can't act with a user's session cookie
// (lib/API/SameOrigin.pm).
//
// Browsers send Sec-Fetch-Site on every request; older ones only send
// Origin, which must then be one of origins. Requests with neither are not
// from a browser page (curl, scripts), so carry no cookie risk.
func sameOrigin(origins []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		site, origin := r.Header.Get("Sec-Fetch-Site"), r.Header.Get("Origin")
		var ok bool
		switch {
		case site != "":
			ok = site == "same-origin" || site == "none"
		case origin != "":
			ok = slices.Contains(origins, origin)
		default:
			ok = true
		}
		if ok {
			next.ServeHTTP(w, r)
			return
		}
		log.Printf("refused cross-site %s %s (Sec-Fetch-Site: %s, Origin: %s)", r.Method, r.URL.Path, orDash(site), orDash(origin))
		respond.Error(w, http.StatusForbidden, "Cross-site request refused")
	})
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
