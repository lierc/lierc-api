// Package api is the JSON API server, ported from lib/API.pm and its roles
// (API::Dispatch, API::Routes, API::Responses) and controllers.
package api

import (
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/mail"
	"github.com/lierc/lierc-api/internal/session"
)

// App holds what handlers share.
type App struct {
	Cfg    config.Config
	DB     *db.DB
	Liercd *liercd.Client
	Mail   mail.Mailer
	Imgur  *http.Client
	// AssetDir is where icon.iconset/ and .apn/ are read from.
	AssetDir string
}

// Request is the per-request state handlers see (API::Request).
type Request struct {
	*http.Request
	// Params holds the route captures.
	Params  map[string]string
	Session *session.Session
	// Body is the whole request body, read before the handler runs.
	Body []byte

	params map[string][]string
}

// Param returns the last value of name among the query parameters followed by
// the urlencoded body parameters, as Plack::Request->parameters->{name}.
func (r *Request) Param(name string) (string, bool) {
	if r.params == nil {
		r.params = map[string][]string{}
		parseURLEncoded(r.params, r.URL.RawQuery)
		ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if ct == "application/x-www-form-urlencoded" {
			parseURLEncoded(r.params, string(r.Body))
		}
	}
	vs := r.params[name]
	if len(vs) == 0 {
		return "", false
	}
	return vs[len(vs)-1], true
}

var (
	pairSep = regexp.MustCompile(`[&;] ?`)
	pctEnc  = regexp.MustCompile(`%[0-9a-fA-F]{2}`)
)

// parseURLEncoded follows WWW::Form::UrlEncoded::PP parse_urlencoded: pairs
// split on [&;] ?, '+' is a space, only valid %XX escapes are decoded.
func parseURLEncoded(into map[string][]string, s string) {
	if s == "" {
		return
	}
	for _, pair := range pairSep.Split(s, -1) {
		pair = strings.ReplaceAll(pair, "+", " ")
		k, v, _ := strings.Cut(pair, "=")
		k, v = pctDecode(k), pctDecode(v)
		into[k] = append(into[k], v)
	}
}

func pctDecode(s string) string {
	return pctEnc.ReplaceAllStringFunc(s, func(m string) string {
		b, _ := strconv.ParseUint(m[1:], 16, 8)
		return string([]byte{byte(b)})
	})
}

// Handler serves one route. A non-nil error becomes a 400 (or a
// StatusError's code), like a die inside API::Dispatch::run's eval.
type Handler func(a *App, r *Request) (Response, error)

// New returns the API handler. It expects session.Codec.Middleware around it
// (main wires proxy.Wrap(trusted, codec.Middleware(api.New(...)))).
func New(cfg config.Config, d *db.DB, lc *liercd.Client, m mail.Mailer) http.Handler {
	return newHandler(&App{
		Cfg: cfg, DB: d, Liercd: lc, Mail: m,
		Imgur:    &http.Client{Timeout: 180 * time.Second}, // LWP::UserAgent default timeout
		AssetDir: ".",
	}, routes)
}

type server struct {
	app    *App
	router router
}

func newHandler(app *App, table []route) http.Handler {
	return &server{app: app, router: compile(table)}
}

// ServeHTTP mirrors API::Dispatch::handle and run.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt, caps := s.router.match(r.Method, r.URL.Path)
	sess := session.FromContext(r.Context())
	if sess == nil {
		sess = &session.Session{}
	}
	req := &Request{Request: r, Params: caps, Session: sess}
	ctx := r.Context()

	if rt == nil || !rt.Public {
		ok := false
		if user := sess.User(); user != "" {
			var err error
			// Outside Perl's eval, a DB failure here is a Plack 500.
			if ok, err = s.app.DB.UserExists(ctx, user); err != nil {
				internalError(w, err)
				return
			}
		}
		if !ok {
			Unauthorized("").Write(w)
			return
		}
	}

	// Perl tests the capture for truth, so an id of "0" skips the check.
	if id := caps["id"]; id != "" && id != "0" {
		ok, err := s.app.DB.VerifyOwner(ctx, id, sess.User())
		if err != nil {
			internalError(w, err)
			return
		}
		if !ok {
			// Dispatch.pm interpolates "$req->captures->{id}" as $req followed by
			// literal text, so Perl's message names the request object.
			Unauthorized(fmt.Sprintf("Invalid connection id 'API::Request=HASH(%p)->captures->{id}'", req)).Write(w)
			return
		}
	}

	if rt == nil || rt.H == nil {
		NotFound().Write(w)
		return
	}

	limit := s.app.Cfg.MaxBodyBytes
	if limit <= 0 {
		limit = config.DefaultMaxBodyBytes
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			msg := "Request entity too large"
			errorStatus(&msg, http.StatusRequestEntityTooLarge).Write(w)
			return
		}
		Error(err.Error()).Write(w)
		return
	}
	req.Body = body

	res := s.run(rt, req)
	if res == nil {
		internalError(w, fmt.Errorf("%s returned no response", rt.Name))
		return
	}
	res.Write(w)
}

// run calls the handler, turning errors and panics into error responses.
func (s *server) run(rt *compiledRoute, req *Request) (res Response) {
	defer func() {
		if p := recover(); p != nil {
			if p == http.ErrAbortHandler {
				panic(p)
			}
			log.Printf("Error handling %s: '%v'", rt.Name, p)
			res = Error(fmt.Sprint(p))
		}
	}()
	res, err := rt.H(s.app, req)
	if err != nil {
		log.Printf("Error handling %s: '%v'", rt.Name, err)
		return errorResponse(err)
	}
	return res
}

func internalError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}
