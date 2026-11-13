package admin

import (
	"net/http"
	"strings"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/login"
)

// User is a user in a list.
type User struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	Email       string     `json:"email"`
	Verified    *bool      `json:"verified"` // null: an account from before verification
	LastLogin   *time.Time `json:"last_login"`
	Registered  *time.Time `json:"registered"`
	Connections int        `json:"connections"`
}

// UserDetail is one user: how the password is stored (never the hash) and
// the connections.
type UserDetail struct {
	User
	Password       string       `json:"password"` // "bcrypt", "legacy" or "none"
	ConnectionList []Connection `json:"connection_list"`
}

func userOf(r db.UserRow) User {
	return User{ID: r.ID, Username: r.Username, Email: r.Email, Verified: r.Verified,
		LastLogin: utc(r.LastLogin), Registered: utc(r.Registered), Connections: r.Connections}
}

func (a *api) userRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/users", a.listUsers)
	mux.HandleFunc("GET /admin/users/lookup", a.lookupUsers)
	mux.HandleFunc("GET /admin/users/{id}", a.userDetail)
	mux.HandleFunc("GET /admin/users/{id}/counts", a.userCounts)
	mux.HandleFunc("GET /admin/users/{id}/logins", a.userLogins)
}

func users(rows []db.UserRow) []User {
	out := make([]User, 0, len(rows))
	for _, r := range rows {
		out = append(out, userOf(r))
	}
	return out
}

func (a *api) listUsers(w http.ResponseWriter, r *http.Request) {
	var f db.UserFilter
	var ok bool
	if f.InactiveSince, ok = sinceParam(w, r); !ok {
		return
	}
	if r.URL.Query().Has("ids") {
		f.IDs = []string{}
		if s := r.URL.Query().Get("ids"); s != "" {
			f.IDs = strings.Split(s, ",")
		}
	}
	rows, err := a.store.ListUsers(r.Context(), f)
	if err != nil {
		internal(w, "list users", err)
		return
	}
	write(w, http.StatusOK, users(rows))
}

func (a *api) lookupUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		fail(w, http.StatusBadRequest, "q is required")
		return
	}
	rows, err := a.store.LookupUsers(r.Context(), q)
	if err != nil {
		internal(w, "look up users", err)
		return
	}
	write(w, http.StatusOK, users(rows))
}

// oneUser finds the user in the path, answering 404 or 500 itself.
func (a *api) oneUser(w http.ResponseWriter, r *http.Request) (db.UserRow, bool) {
	rows, err := a.store.ListUsers(r.Context(), db.UserFilter{IDs: []string{r.PathValue("id")}})
	if err != nil {
		internal(w, "get user", err)
		return db.UserRow{}, false
	}
	if len(rows) == 0 {
		fail(w, http.StatusNotFound, "no such user")
		return db.UserRow{}, false
	}
	return rows[0], true
}

func (a *api) userDetail(w http.ResponseWriter, r *http.Request) {
	u, ok := a.oneUser(w, r)
	if !ok {
		return
	}
	conns, err := a.store.ListConnections(r.Context(), db.ConnectionFilter{User: u.ID})
	if err != nil {
		internal(w, "list connections", err)
		return
	}
	d := UserDetail{User: userOf(u), Password: u.Password, ConnectionList: []Connection{}}
	for _, c := range conns {
		d.ConnectionList = append(d.ConnectionList, connectionOf(c))
	}
	write(w, http.StatusOK, d)
}

func (a *api) userCounts(w http.ResponseWriter, r *http.Request) {
	u, ok := a.oneUser(w, r)
	if !ok {
		return
	}
	counts, err := a.store.UserCounts(r.Context(), u.ID)
	if err != nil {
		internal(w, "count user rows", err)
		return
	}
	write(w, http.StatusOK, counts)
}

// Login is one of a user's logins.
type Login struct {
	ID        int64     `json:"id"`
	Created   time.Time `json:"created"`
	LastUsed  time.Time `json:"last_used"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
}

func (a *api) userLogins(w http.ResponseWriter, r *http.Request) {
	u, ok := a.oneUser(w, r)
	if !ok {
		return
	}
	infos, err := login.List(r.Context(), a.store, u.ID)
	if err != nil {
		internal(w, "list logins", err)
		return
	}
	out := make([]Login, 0, len(infos))
	for _, l := range infos {
		out = append(out, Login{ID: l.ID, Created: l.Created.UTC().Truncate(time.Second),
			LastUsed: l.LastUsed.UTC().Truncate(time.Second), IP: l.IP, UserAgent: l.UserAgent})
	}
	write(w, http.StatusOK, out)
}
