package testenv

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ImgurUpload is one file received by the fake Imgur upload endpoint.
type ImgurUpload struct {
	Field, Filename, Content, Authorization string
}

// FakeImgur starts a stand-in for api.imgur.com. POST /3/upload answers 200
// with link https://i.imgur.com/<filename> and deletehash "hash-<filename>";
// a file named "fail" gets a 429 instead. DELETE /3/image/<hash> answers 200
// unless the hash is "hash-fail".
func FakeImgur(t testing.TB) (baseURL string, uploads func() []ImgurUpload) {
	var mu sync.Mutex
	var got []ImgurUpload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Imgur", "yes")
		switch {
		case r.Method == "POST" && r.URL.Path == "/3/upload":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			name := ""
			mu.Lock()
			for field, fhs := range r.MultipartForm.File {
				for _, fh := range fhs {
					f, _ := fh.Open()
					b, _ := io.ReadAll(f)
					f.Close()
					got = append(got, ImgurUpload{field, fh.Filename, string(b), r.Header.Get("Authorization")})
					name = fh.Filename
				}
			}
			mu.Unlock()
			if name == "fail" {
				w.WriteHeader(429)
				io.WriteString(w, `{"success":false}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]string{
				"link": "https://i.imgur.com/" + name, "deletehash": "hash-" + name}})
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/3/image/"):
			if r.URL.Path == "/3/image/hash-fail" {
				w.WriteHeader(403)
				io.WriteString(w, `{"success":false}`)
				return
			}
			io.WriteString(w, `{"success":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []ImgurUpload {
		mu.Lock()
		defer mu.Unlock()
		return append([]ImgurUpload(nil), got...)
	}
}
