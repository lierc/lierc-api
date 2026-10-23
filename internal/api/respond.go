package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Response is what a handler returns; Write sends it (lib/API/Responses.pm).
type Response interface {
	Write(w http.ResponseWriter)
}

// StatusError is a handler error answered with Code instead of 400.
type StatusError struct {
	Code int
	Msg  string
}

func (e *StatusError) Error() string { return e.Msg }

type jsonResponse struct {
	code int
	body []byte
}

func (j jsonResponse) Write(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "application/javascript;charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(j.body)))
	w.WriteHeader(j.code)
	w.Write(j.body)
}

// JSON is a 200 JSON response.
func JSON(v any) Response { return JSONStatus(v, http.StatusOK) }

// JSONStatus encodes v like JSON::XS encode_json: no HTML escaping, no
// trailing newline. An encoding failure becomes a 400, as a die inside the
// handler's eval would.
func JSONStatus(v any, code int) Response {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return Error(err.Error())
	}
	return jsonResponse{code: code, body: bytes.TrimSuffix(buf.Bytes(), []byte("\n"))}
}

// OK is {"status":"ok"} merged with the key/value pairs in extra.
func OK(extra ...any) Response {
	if len(extra)%2 != 0 {
		panic("api.OK: odd number of key/value arguments")
	}
	m := map[string]any{"status": "ok"}
	for i := 0; i < len(extra); i += 2 {
		k, ok := extra[i].(string)
		if !ok {
			panic(fmt.Sprintf("api.OK: key %v is not a string", extra[i]))
		}
		m[k] = extra[i+1]
	}
	return JSON(m)
}

type noContent struct{}

func (noContent) Write(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// NoContent is a 204 with no headers and an empty body.
func NoContent() Response { return noContent{} }

type errorBody struct {
	Status string  `json:"status"`
	Error  *string `json:"error"`
}

// errorStatus builds {"status":"error","error":msg}; a nil msg is JSON null.
func errorStatus(msg *string, code int) Response {
	if msg != nil {
		// API::Responses::error: s/\n+$//
		s := strings.TrimRight(*msg, "\n")
		msg = &s
	}
	return JSONStatus(errorBody{Status: "error", Error: msg}, code)
}

// Error is a 400 {"status":"error","error":msg}.
func Error(msg string) Response { return errorStatus(&msg, http.StatusBadRequest) }

// NotFound is a 404 "not found".
func NotFound() Response {
	msg := "not found"
	return errorStatus(&msg, http.StatusNotFound)
}

// Unauthorized is a 401 with msg, or "unauthorized" when msg is empty.
func Unauthorized(msg string) Response {
	if msg == "" {
		msg = "unauthorized"
	}
	return errorStatus(&msg, http.StatusUnauthorized)
}

// ErrorFromDB is a 400 for err. For a Postgres error the message is only its
// DETAIL, or null when there is none (API::Responses::error on DBD:: errors).
func ErrorFromDB(err error) Response {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		if pg.Detail == "" {
			return errorStatus(nil, http.StatusBadRequest)
		}
		return Error(pg.Detail)
	}
	return Error(err.Error())
}

// errorResponse maps a handler error to its response.
func errorResponse(err error) Response {
	var se *StatusError
	if errors.As(err, &se) {
		return errorStatus(&se.Msg, se.Code)
	}
	return ErrorFromDB(err)
}

type passthrough struct {
	status int
	header http.Header
	body   []byte
}

// hopHeaders are not copied from an upstream response.
var hopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Connection", "Transfer-Encoding",
	"Upgrade", "Te", "Trailer", "Content-Length",
}

func (p passthrough) Write(w http.ResponseWriter) {
	h := w.Header()
	for k, vs := range p.header {
		h[k] = append([]string(nil), vs...)
	}
	for _, k := range hopHeaders {
		h.Del(k)
	}
	h.Set("Content-Length", strconv.Itoa(len(p.body)))
	w.WriteHeader(p.status)
	w.Write(p.body)
}

// Passthrough sends an upstream response's status, headers and body
// (API::Responses::passthrough).
func Passthrough(status int, header http.Header, body []byte) Response {
	return passthrough{status: status, header: header, body: body}
}

type stream struct {
	fn func(w io.Writer) bool
}

type flushWriter struct {
	w http.ResponseWriter
	f *http.ResponseController
}

func (fw flushWriter) Write(b []byte) (int, error) {
	n, err := fw.w.Write(b)
	if err != nil {
		return n, err
	}
	if err := fw.f.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return n, err
	}
	return n, nil
}

func (s stream) Write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream;charset=utf-8")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	rc.Flush()
	s.fn(flushWriter{w: w, f: rc})
}

// Stream sends a 200 text/event-stream (API::Responses::event_stream) and
// runs fn with a writer that flushes every write. fn returns false when it
// stopped early because a write failed; the stream ends either way.
func Stream(fn func(w io.Writer) bool) Response { return stream{fn: fn} }
