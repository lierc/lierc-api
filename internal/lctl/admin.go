package lctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lierc/lierc-api/internal/admin"
)

// Admin is a client for the API's JSON admin server (API_ADMIN).
type Admin struct {
	BaseURL string
	HTTP    *http.Client
	// Timeout bounds every request but the two deletes, which can run as
	// long as they need to.
	Timeout time.Duration
}

// ErrNotFound is the admin API answering 404.
var ErrNotFound = errors.New("not found")

// ErrUnreachable is the admin server not answering at all.
var ErrUnreachable = errors.New("the API's admin server isn't reachable")

// unreachable is an ErrUnreachable that says why. Commands that change
// things didn't, since the request never arrived.
type unreachable struct{ err error }

func (e *unreachable) Error() string {
	return fmt.Sprintf("%s (%v); nothing was changed", ErrUnreachable.Error(), e.err)
}
func (e *unreachable) Is(target error) bool { return target == ErrUnreachable }
func (e *unreachable) Unwrap() error        { return e.err }

// NewAdmin is a client for the admin server at base.
func NewAdmin(base string) *Admin {
	return &Admin{BaseURL: strings.TrimSuffix(base, "/"), HTTP: &http.Client{}, Timeout: 5 * time.Minute}
}

// do sends a request and decodes a 200 JSON answer into out (nil to ignore it).
func (a *Admin) do(ctx context.Context, method, path string, query url.Values, out any) error {
	return a.send(ctx, a.Timeout, method, path, query, out)
}

// send is do with its own bound; 0 means none.
func (a *Admin) send(ctx context.Context, timeout time.Duration, method, path string, query url.Values, out any) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	u := strings.TrimSuffix(a.BaseURL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	res, err := a.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return &unreachable{err}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode == http.StatusOK {
		if out == nil {
			return nil
		}
		return json.Unmarshal(body, out)
	}
	if res.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	var e admin.Error
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return errors.New(e.Error)
	}
	return fmt.Errorf("the admin API said %s", statusLine(res.StatusCode))
}

func sinceQuery(since *time.Time) url.Values {
	q := url.Values{}
	if since != nil {
		q.Set("inactive_since", since.UTC().Format(time.RFC3339))
	}
	return q
}

// Users lists users, never-logged-in first then oldest login; with
// inactiveSince, only those who haven't logged in since then.
func (a *Admin) Users(ctx context.Context, inactiveSince *time.Time) ([]admin.User, error) {
	var out []admin.User
	return out, a.do(ctx, http.MethodGet, "/admin/users", sinceQuery(inactiveSince), &out)
}

// UsersByID lists the users with these ids (those that exist).
func (a *Admin) UsersByID(ctx context.Context, ids []string) ([]admin.User, error) {
	var out []admin.User
	return out, a.do(ctx, http.MethodGet, "/admin/users", url.Values{"ids": {strings.Join(ids, ",")}}, &out)
}

// LookupUsers finds users whose id, username or email is q.
func (a *Admin) LookupUsers(ctx context.Context, q string) ([]admin.User, error) {
	var out []admin.User
	return out, a.do(ctx, http.MethodGet, "/admin/users/lookup", url.Values{"q": {q}}, &out)
}

// User is one user with their connections; ErrNotFound if there is none.
func (a *Admin) User(ctx context.Context, id string) (admin.UserDetail, error) {
	var out admin.UserDetail
	return out, a.do(ctx, http.MethodGet, "/admin/users/"+url.PathEscape(id), nil, &out)
}

// UserCounts counts, by table, the rows deleting the user would remove.
func (a *Admin) UserCounts(ctx context.Context, id string) (map[string]int64, error) {
	var out map[string]int64
	return out, a.do(ctx, http.MethodGet, "/admin/users/"+url.PathEscape(id)+"/counts", nil, &out)
}

// instanceOf is the liercd instance a connection runs on, for routing
// liercd calls: "next", or "" for main (and for a connection that's gone).
func (a *Admin) instanceOf(ctx context.Context, id string) (string, error) {
	c, err := a.Connection(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if c.Instance == "next" {
		return "next", nil
	}
	return "", err
}

// Connections lists connections, by their owner's last login; instance is
// "", "main" or "next".
func (a *Admin) Connections(ctx context.Context, instance string, inactiveSince *time.Time) ([]admin.Connection, error) {
	q := sinceQuery(inactiveSince)
	if instance != "" {
		q.Set("instance", instance)
	}
	var out []admin.Connection
	return out, a.do(ctx, http.MethodGet, "/admin/connections", q, &out)
}

// Connection is one connection (its config without the server password);
// ErrNotFound if there is none.
func (a *Admin) Connection(ctx context.Context, id string) (admin.ConnectionDetail, error) {
	var out admin.ConnectionDetail
	return out, a.do(ctx, http.MethodGet, "/admin/connections/"+url.PathEscape(id), nil, &out)
}

// ConnectionConfig is a connection's saved config whole, password and all.
func (a *Admin) ConnectionConfig(ctx context.Context, id string) (json.RawMessage, error) {
	var out json.RawMessage
	return out, a.do(ctx, http.MethodGet, "/admin/connections/"+url.PathEscape(id)+"/config", nil, &out)
}

// Logins lists a user's logins, most recently used first.
func (a *Admin) Logins(ctx context.Context, user string) ([]admin.Login, error) {
	var out []admin.Login
	return out, a.do(ctx, http.MethodGet, "/admin/users/"+url.PathEscape(user)+"/logins", nil, &out)
}

// LastLogID is the highest id in the log (0 if it's empty).
func (a *Admin) LastLogID(ctx context.Context) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	return out.ID, a.do(ctx, http.MethodGet, "/admin/log/last", nil, &out)
}

// StaleSeen counts the seen rows prune-seen would delete.
func (a *Admin) StaleSeen(ctx context.Context) (int64, error) {
	var out struct {
		Count int64 `json:"count"`
	}
	return out.Count, a.do(ctx, http.MethodGet, "/admin/seen/stale", nil, &out)
}

// ErrMaybeDone is a delete's request ending without an answer: it may have
// reached the API and been done.
var ErrMaybeDone = errors.New("the request ended before the API answered; the delete may have finished")

// writeResult is the body of every successful write.
type writeResult struct {
	Counts map[string]int64 `json:"counts"`
	Ended  int64            `json:"ended"`
	Pruned int64            `json:"pruned"`
	Swept  int64            `json:"swept"`
	// SweepError is set when the delete committed but its late-log sweep failed.
	SweepError string `json:"sweep_error"`
}

// Deleted is what a delete did. Counts are the delete's own rows per table;
// Swept is the late log rows removed after it. SweepError, if set, says the
// delete is done but the sweep failed.
type Deleted struct {
	Counts     map[string]int64
	Swept      int64
	SweepError string
}

// remove sends a delete; logAfter is the log's last id from before the
// command began destroying connections.
func (a *Admin) remove(ctx context.Context, path string, logAfter int64) (Deleted, error) {
	var out writeResult
	err := a.send(ctx, 0, http.MethodDelete, path, url.Values{"log_after": {strconv.FormatInt(logAfter, 10)}}, &out)
	if errors.Is(err, ErrUnreachable) {
		// the caller has already reached the server (LastLogID), so this
		// request may have arrived before the connection broke
		return Deleted{}, ErrMaybeDone
	}
	return Deleted{out.Counts, out.Swept, out.SweepError}, err
}

// DeleteUser deletes a user and their data, then sweeps log rows with an id
// above logAfter. It returns what was deleted. ErrNotFound if there is no such user.
func (a *Admin) DeleteUser(ctx context.Context, id string, logAfter int64) (Deleted, error) {
	return a.remove(ctx, "/admin/users/"+url.PathEscape(id), logAfter)
}

// DeleteConnection deletes a connection and its data like DeleteUser.
func (a *Admin) DeleteConnection(ctx context.Context, id string, logAfter int64) (Deleted, error) {
	return a.remove(ctx, "/admin/connections/"+url.PathEscape(id), logAfter)
}

// EndLogin ends one login of a user; ErrNotFound if there is no such login.
func (a *Admin) EndLogin(ctx context.Context, user string, id int64) error {
	return a.do(ctx, http.MethodDelete, "/admin/users/"+url.PathEscape(user)+"/logins/"+strconv.FormatInt(id, 10), nil, nil)
}

// EndLogins ends every login of a user and returns how many there were.
func (a *Admin) EndLogins(ctx context.Context, user string) (int64, error) {
	var out writeResult
	return out.Ended, a.do(ctx, http.MethodDelete, "/admin/users/"+url.PathEscape(user)+"/logins", nil, &out)
}

// PruneLogins deletes the logins unused for 7 days and returns how many.
func (a *Admin) PruneLogins(ctx context.Context) (int64, error) {
	var out writeResult
	return out.Pruned, a.do(ctx, http.MethodPost, "/admin/logins/prune", nil, &out)
}

// PruneSeen deletes the stale seen rows and returns how many.
func (a *Admin) PruneSeen(ctx context.Context) (int64, error) {
	var out writeResult
	return out.Pruned, a.do(ctx, http.MethodPost, "/admin/seen/prune", nil, &out)
}
