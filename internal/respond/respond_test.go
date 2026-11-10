package respond

import (
	"net/http/httptest"
	"testing"
)

func TestMarshalNoHTMLEscape(t *testing.T) {
	b, err := Marshal(map[string]string{"a": "<&>"})
	if err != nil || string(b) != `{"a":"<&>"}` {
		t.Fatalf("Marshal = %s, %v", b, err)
	}
}

func TestError(t *testing.T) {
	w := httptest.NewRecorder()
	Error(w, 400, "x")
	if w.Code != 400 || w.Body.String() != `{"status":"error","error":"x"}` {
		t.Errorf("got %d %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/javascript;charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	if cl := w.Header().Get("Content-Length"); cl != "30" {
		t.Errorf("content length %q", cl)
	}
}
