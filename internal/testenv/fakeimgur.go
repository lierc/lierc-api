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

// FakeImgur starts a stand-in for api.imgur.com (see Imgur) and returns its
// base URL and an accessor for the uploads it has received.
func FakeImgur(t testing.TB) (baseURL string, uploads func() []ImgurUpload) {
	f := NewImgur()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv.URL, f.Uploads
}

// Imgur stands in for api.imgur.com. POST /3/upload answers 200 with link
// https://i.imgur.com/<filename> and deletehash "hash-<filename>"; a file
// named "fail" gets a 429 instead. DELETE /3/image/<hash> answers 200 unless
// the hash is "hash-fail".
type Imgur struct {
	mu  sync.Mutex
	got []ImgurUpload
}

// NewImgur returns an Imgur that is not listening; serve it with any
// http.Server.
func NewImgur() *Imgur { return &Imgur{} }

// Uploads returns the files received so far.
func (f *Imgur) Uploads() []ImgurUpload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ImgurUpload(nil), f.got...)
}

// Reset forgets the uploads received so far.
func (f *Imgur) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = nil
}

func (f *Imgur) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Imgur", "yes")
	switch {
	case r.Method == "POST" && r.URL.Path == "/3/upload":
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		name := ""
		f.mu.Lock()
		for field, fhs := range r.MultipartForm.File {
			for _, fh := range fhs {
				file, _ := fh.Open()
				b, _ := io.ReadAll(file)
				file.Close()
				f.got = append(f.got, ImgurUpload{field, fh.Filename, string(b), r.Header.Get("Authorization")})
				name = fh.Filename
			}
		}
		f.mu.Unlock()
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
}
