package api

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lierc/lierc-api/internal/accounts"
	"github.com/lierc/lierc-api/internal/auth"
	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/login"
	"github.com/lierc/lierc-api/internal/mail"
	"github.com/lierc/lierc-api/internal/respond"
)

// newMailer builds the mailer for the accounts service; tests replace it.
// A bad SMTP_OPTS is logged and leaves no mailer, so the API still starts
// (accounts logs the emails it can't send).
var newMailer = func(cfg config.Config) accounts.Mailer {
	opts, err := mail.ParseOpts(cfg.SMTPOpts)
	if err != nil {
		log.Printf("SMTP_OPTS: %v; emails will not be sent", err)
		return nil
	}
	return &mail.Sender{Opts: opts, From: cfg.FromAddress}
}

func (a *api) accounts(mux *http.ServeMux) {
	a.acct = &accounts.Service{
		DB:             a.pool,
		Conns:          a.conns,
		Mail:           newMailer(a.cfg),
		Secret:         a.cfg.Secret,
		SiteURL:        a.cfg.SiteURL,
		NoSMTP:         a.cfg.NoSMTP,
		DefaultNetwork: a.cfg.DefaultNetwork,
		After:          func(f func()) { go f() },
	}
	mux.Handle("GET /auth", a.users.RequireAny(a.showUser))
	mux.HandleFunc("POST /auth", a.login)
	mux.HandleFunc("POST /register", a.register)
	mux.HandleFunc("POST /logout", a.logout)
	mux.HandleFunc("POST /verify", a.verify)
	mux.Handle("POST /verify/resend", a.users.RequireAny(a.resend))
	mux.HandleFunc("POST /reset", a.reset)
	mux.HandleFunc("POST /reset/confirm", a.resetConfirm)
	mux.HandleFunc("POST /unsubscribe", a.unsubscribe)
	mux.HandleFunc("GET /unsubscribe", unsubscribePage)
}

// userBody is GET /auth's answer.
type userBody struct {
	Email    string `json:"email"`
	User     string `json:"user"`
	ID       string `json:"id"`
	Verified *bool  `json:"verified"`
}

func userJSON(u accounts.User) userBody {
	return userBody{Email: u.Email, User: u.Username, ID: u.ID, Verified: u.Verified}
}

// accountFailed answers a user-facing accounts error with its status and
// message, and anything else as a 500.
func accountFailed(w http.ResponseWriter, what string, err error) {
	if e, ok := accounts.IsError(err); ok {
		respond.Error(w, e.Code, e.Msg)
		return
	}
	respond.Internal(w, what, err)
}

func (a *api) showUser(w http.ResponseWriter, r *http.Request, id string) {
	u, err := a.acct.Show(r.Context(), id)
	if err != nil {
		accountFailed(w, "show user", err)
		return
	}
	respond.JSON(w, http.StatusOK, userJSON(u))
}

// loggedIn logs u in, answering as GET /auth does.
// The request's own login, if it has one, is ended first, so logging in
// again replaces a login instead of piling them up.
// deleteToken is login.DeleteToken, replaceable in tests.
var deleteToken = login.DeleteToken

func (a *api) loggedIn(w http.ResponseWriter, r *http.Request, u accounts.User) {
	if c, err := r.Cookie(login.CookieName); err == nil && login.IsToken(c.Value) {
		// the old login only matters to this browser, which is replacing
		// it; if it can't be ended now it expires unused
		if err := deleteToken(r.Context(), a.pool, c.Value); err != nil {
			log.Printf("end old login: %v", err)
		}
	}
	agent, ip := auth.Client(r)
	token, err := login.Create(r.Context(), a.pool, u.ID, agent, ip)
	if err != nil {
		respond.Internal(w, "create login", err)
		return
	}
	login.SetCookie(w, token, a.cfg.Secure, time.Now())
	respond.JSON(w, http.StatusOK, userJSON(u))
}

func (a *api) login(w http.ResponseWriter, r *http.Request) {
	param, ok := formParams(w, r)
	if !ok {
		return
	}
	key := limitKey(param("email"))
	if key != "" {
		if allowed, retry := a.loginLimit.Take(key); !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(retry.Seconds())))))
			respond.Error(w, http.StatusTooManyRequests, msgTooMany)
			return
		}
	}
	u, err := a.acct.Login(r.Context(), param("email"), param("pass"))
	// only a wrong password or unknown account counts as a failure
	if e, isUser := accounts.IsError(err); key != "" && !(isUser && e.Code == http.StatusUnauthorized) {
		a.loginLimit.Return(key)
	}
	if err != nil {
		accountFailed(w, "login", err)
		return
	}
	a.loggedIn(w, r, u)
}

const msgTooMany = "Too many login attempts — try again in a few minutes"

// limitKey is the rate limits' key for a submitted email or username:
// trimmed and lowercased, and a hash of that when it's longer than any
// real email, so a flood of huge fields can't fill memory.
func limitKey(s string) string {
	k := strings.ToLower(strings.TrimSpace(s))
	if len(k) > maxKeyLen {
		sum := sha256.Sum256([]byte(k))
		return hex.EncodeToString(sum[:])
	}
	return k
}

// maxKeyLen is the longest email address allowed (RFC 5321).
const maxKeyLen = 254

func (a *api) register(w http.ResponseWriter, r *http.Request) {
	param, ok := formParams(w, r)
	if !ok {
		return
	}
	u, err := a.acct.Register(r.Context(), param("username"), param("email"), param("pass"))
	if err != nil {
		accountFailed(w, "register", err)
		return
	}
	a.loggedIn(w, r, u)
}

func (a *api) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(login.CookieName); err == nil && login.IsToken(c.Value) {
		if err := login.DeleteToken(r.Context(), a.pool, c.Value); err != nil {
			log.Printf("logout: %v", err)
		}
	}
	login.ExpireCookie(w, a.cfg.Secure)
	ok(w)
}

func (a *api) verify(w http.ResponseWriter, r *http.Request) {
	param, good := formParams(w, r)
	if !good {
		return
	}
	if err := a.acct.Verify(r.Context(), param("token")); err != nil {
		accountFailed(w, "verify", err)
		return
	}
	ok(w)
}

func (a *api) resend(w http.ResponseWriter, r *http.Request, id string) {
	if err := a.acct.Resend(r.Context(), id); err != nil {
		accountFailed(w, "resend verify", err)
		return
	}
	ok(w)
}

func (a *api) reset(w http.ResponseWriter, r *http.Request) {
	param, good := formParams(w, r)
	if !good {
		return
	}
	// over the limit, the answer is the same and no email goes out
	if email := param("email"); accounts.IsEmail(email) {
		if allowed, _ := a.resetLimit.Take(limitKey(email)); !allowed {
			ok(w)
			return
		}
	}
	if err := a.acct.Reset(r.Context(), param("email")); err != nil {
		accountFailed(w, "reset", err)
		return
	}
	ok(w)
}

func (a *api) resetConfirm(w http.ResponseWriter, r *http.Request) {
	param, good := formParams(w, r)
	if !good {
		return
	}
	u, err := a.acct.ResetConfirm(r.Context(), param("token"), param("pass"))
	if err != nil {
		accountFailed(w, "reset confirm", err)
		return
	}
	a.loggedIn(w, r, u)
}

// unsubscribe turns email notifications off for the user an emailed
// unsubscribe token names. It needs no login: it's posted by the
// unsubscribe page, or by a mail provider's one-click button
// (List-Unsubscribe-Post), with the token in the query. A user who no
// longer exists gets ok too: nothing will be emailed.
func (a *api) unsubscribe(w http.ResponseWriter, r *http.Request) {
	param, good := formParams(w, r)
	if !good {
		return
	}
	user, valid := accounts.CheckUnsubscribeToken(a.cfg.UnsubscribeSecret, param("token"))
	if !valid {
		respond.Error(w, http.StatusBadRequest, "This unsubscribe link isn't valid.")
		return
	}
	exists, err := a.db.UserExists(r.Context(), user)
	if err != nil {
		respond.Internal(w, "unsubscribe", err)
		return
	}
	if exists {
		if err := a.db.SetPref(r.Context(), user, "email", "false"); err != nil {
			respond.Internal(w, "unsubscribe", err)
			return
		}
	}
	ok(w)
}

// unsubscribePage sends a mail client that opens the List-Unsubscribe URL,
// instead of posting to it, to the unsubscribe page, which asks first. It
// changes nothing. Only a token of base64url characters is passed on, so
// nothing else can reach the Location header.
func unsubscribePage(w http.ResponseWriter, r *http.Request) {
	loc := "/unsubscribe/"
	if t := r.URL.Query().Get("token"); t != "" && strings.Trim(t, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") == "" {
		loc += "#" + t
	}
	w.Header().Set("Location", loc)
	w.WriteHeader(http.StatusSeeOther)
}
