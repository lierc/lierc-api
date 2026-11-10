package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSameOrigin(t *testing.T) {
	ran := false
	h := sameOrigin([]string{"https://ok"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		io.WriteString(w, "ran")
	}))
	for _, c := range []struct {
		method, site, origin string
		want                 int
	}{
		{"GET", "cross-site", "", 200},
		{"HEAD", "cross-site", "", 200},
		{"OPTIONS", "cross-site", "", 200},
		{"POST", "same-origin", "", 200},
		{"POST", "none", "", 200},
		{"POST", "same-site", "", 403},
		{"POST", "cross-site", "", 403},
		{"POST", "", "https://ok", 200},
		{"POST", "", "https://evil", 403},
		{"POST", "", "", 200},
		{"DELETE", "", "https://evil", 403},
		{"PUT", "", "https://evil", 403},
		{"POST", "same-origin", "https://evil", 200},
	} {
		ran = false
		req := httptest.NewRequest(c.method, "/x", nil)
		if c.site != "" {
			req.Header.Set("Sec-Fetch-Site", c.site)
		}
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Errorf("%+v: %d", c, w.Code)
		}
		if c.want == 403 {
			if ran || w.Body.String() != `{"status":"error","error":"Cross-site request refused"}` ||
				w.Header().Get("Content-Type") != "application/javascript;charset=utf-8" {
				t.Errorf("%+v: ran=%v %q %s", c, ran, w.Header().Get("Content-Type"), w.Body)
			}
		} else if !ran {
			t.Errorf("%+v: handler didn't run", c)
		}
	}
}
