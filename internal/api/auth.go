package api

import (
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/lierc/lierc-api/internal/password"
)

// Handlers ported from lib/API/Controller/Auth.pm.

// authTemplate is $auth_template; %s are the name and link.
const authTemplate = `Hello %s,

Thank you for signing up with relaychat.party!

Please click the following link to validate your email address:
%s

You must visit this link in the next day, or your account will
be deleted from the system.

Happy chatting!`

const verifySubject = "Please verify your relaychat.party account"

// xslateEscape is Text::Xslate's default HTML escaping of <: $var :>.
var xslateEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")

func renderAuthTemplate(name, link string) string {
	e := xslateEscape.Replace
	return strings.Replace(strings.Replace(authTemplate, "%s", e(name), 1), "%s", e(link), 1)
}

func authHealth(a *App, r *Request) (Response, error) {
	if err := a.DB.Ping(r.Context()); err != nil {
		return nil, err
	}
	return OK(), nil
}

func authShow(a *App, r *Request) (Response, error) {
	u, err := a.DB.LookupUser(r.Context(), r.Session.User())
	if err != nil {
		return nil, err
	}
	// A missing user gives nulls, as Perl's autovivified undef hash does.
	var email, user, id *string
	if u != nil {
		email, user, id = &u.Email, &u.Username, &u.ID
	}
	return JSON(map[string]any{"email": email, "user": user, "id": id}), nil
}

func authLogin(a *App, r *Request) (Response, error) {
	pass, _ := r.Param("pass")
	email, _ := r.Param("email")
	hashed, err := password.Hash(pass, a.Cfg.Secret)
	if err != nil {
		return nil, err
	}
	var id string
	err = a.DB.Pool.QueryRow(r.Context(),
		`SELECT id FROM "user" WHERE (email=$1 OR username=$1) AND password=$2`, email, hashed).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Unauthorized("Invalid email or password"), nil
	}
	if err != nil {
		return nil, err
	}
	r.Session.SetUser(id)
	return authShow(a, r)
}

func authLogout(a *App, r *Request) (Response, error) {
	r.Session.Clear()
	return OK(), nil
}

// hasNonSpace is /\S/ (Perl's \s also matches \v).
func hasNonSpace(s string) bool {
	return strings.Trim(s, " \t\n\v\f\r") != ""
}

func authRegister(a *App, r *Request) (Response, error) {
	for _, field := range []string{"username", "email", "pass"} {
		if v, ok := r.Param(field); !ok || !hasNonSpace(v) {
			return Error(field + " is required"), nil
		}
	}
	email, _ := r.Param("email")
	pass, _ := r.Param("pass")
	user, _ := r.Param("username")

	if !isEmail(email) {
		return Error("Invalid email address"), nil
	}

	hashed, err := password.Hash(pass, a.Cfg.Secret)
	if err != nil {
		return nil, err
	}
	id, token, err := a.DB.AddUser(r.Context(), user, email, hashed)
	if err != nil {
		return nil, err
	}
	// Set before mailing: a failed send still leaves the session logged in, as
	// Plack::Session saves it regardless of the die (Auth.pm register).
	r.Session.SetUser(id)

	if !a.Cfg.NoSMTP {
		body := renderAuthTemplate(user, "https://relaychat.party/api/verify?"+token)
		if err := a.Mail.Send(email, verifySubject, body); err != nil {
			return nil, err
		}
	}
	return authShow(a, r)
}

func authVerify(a *App, r *Request) (Response, error) {
	query := r.URL.RawQuery
	var id string
	err := a.DB.Pool.QueryRow(r.Context(),
		`SELECT id FROM "user" WHERE verify_token=$1 LIMIT 1`, query).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// selectcol_arrayref returns an empty arrayref, which is truthy, so
		// Auth.pm answers ok and updates WHERE id=NULL (nothing).
		return OK(), nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := a.DB.Pool.Exec(r.Context(), `UPDATE "user" SET verified=true WHERE id=$1`, id); err != nil {
		return nil, err
	}
	return OK(), nil
}

func authToken(a *App, r *Request) (Response, error) {
	user := r.Session.User()
	token, err := a.DB.GetToken(r.Context(), user)
	if err != nil {
		return nil, err
	}
	extra := make([]string, 6)
	for i := range extra {
		if extra[i], err = a.DB.GetToken(r.Context(), user); err != nil {
			return nil, err
		}
	}
	return JSON(map[string]any{"token": token, "extra": extra}), nil
}

var (
	emailLocal = regexp.MustCompile(`\A[A-Za-z0-9!#$%&'*+/=?^_` + "`" + `{|}~-]+(\.[A-Za-z0-9!#$%&'*+/=?^_` + "`" + `{|}~-]+)*\z`)
	domainLbl  = regexp.MustCompile(`\A[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\z`)
	tldLabel   = regexp.MustCompile(`\A[A-Za-z]{2,63}\z`)
)

// isEmail approximates Data::Validate::Email::is_email: an RFC 822 style
// dot-atom local part and a dotted hostname whose last label is alphabetic.
// It does not carry Data::Validate::Domain's list of known TLDs.
func isEmail(s string) bool {
	if len(s) > 254 {
		return false
	}
	at := strings.LastIndexByte(s, '@')
	if at < 1 {
		return false
	}
	local, domain := s[:at], s[at+1:]
	if len(local) > 64 || !emailLocal.MatchString(local) {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !domainLbl.MatchString(l) {
			return false
		}
	}
	return tldLabel.MatchString(labels[len(labels)-1])
}
