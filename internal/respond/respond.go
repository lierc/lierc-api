// Package respond writes responses in the Perl API's shape
// (lib/API/Responses.pm).
package respond

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
)

// Marshal encodes like JSON::XS encode_json: no HTML escaping, no newline.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// JSON is API::Responses json.
func JSON(w http.ResponseWriter, code int, v any) {
	b, err := Marshal(v)
	if err != nil {
		Internal(w, "encode response", err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/javascript;charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(code)
	w.Write(b)
}

type errorBody struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

// Error is API::Responses error.
func Error(w http.ResponseWriter, code int, msg string) {
	JSON(w, code, errorBody{"error", msg})
}

// Internal logs what failed and answers 500, without the error's detail.
func Internal(w http.ResponseWriter, what string, err error) {
	log.Printf("%s: %v", what, err)
	Error(w, http.StatusInternalServerError, "internal error")
}
