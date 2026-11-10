package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/respond"
)

// The simple reads, ported from lib/API/Controller/*.pm.
func (a *api) reads(mux *http.ServeMux) {
	mux.Handle("GET /preference", a.users.Require(a.preference))
	mux.Handle("GET /privates", a.users.Require(a.privates))
	mux.Handle("GET /seen", a.users.Require(a.seen))
	mux.Handle("GET /missed", a.users.Require(a.missed))
	mux.Handle("GET /notification/web_push_keys", a.users.Require(a.webPushKeys))
	mux.Handle("GET /connection/{id}/channel", a.users.Require(a.owned(a.channels)))
}

// list answers v, or 500 for err.
func list(w http.ResponseWriter, what string, v any, err error) {
	if err != nil {
		respond.Internal(w, what, err)
		return
	}
	respond.JSON(w, http.StatusOK, v)
}

func (a *api) preference(w http.ResponseWriter, r *http.Request, user string) {
	prefs, err := a.db.Prefs(r.Context(), user)
	list(w, "preference", prefs, err)
}

func (a *api) privates(w http.ResponseWriter, r *http.Request, user string) {
	privates, err := a.db.Privates(r.Context(), user)
	list(w, "privates", privates, err)
}

func (a *api) seen(w http.ResponseWriter, r *http.Request, user string) {
	seen, err := a.db.Seen(r.Context(), user)
	list(w, "seen", seen, err)
}

// missed counts what each <connection>-<channel>=<message id> query
// parameter's channel has had since that message.
func (a *api) missed(w http.ResponseWriter, r *http.Request, user string) {
	counts, err := a.db.Missed(r.Context(), user, parseMissed(r.URL.Query()))
	list(w, "missed", counts, err)
}

// parseMissed reads /missed's parameters. A key without a "-", or a value
// that isn't a message id, is skipped; for a repeated key the last value
// wins, as with Perl's parameters.
func parseMissed(q url.Values) []db.MissedEntry {
	var es []db.MissedEntry
	for key, vals := range q {
		conn, channel, ok := strings.Cut(key, "-")
		if !ok || len(vals) == 0 {
			continue
		}
		after, err := strconv.ParseInt(vals[len(vals)-1], 10, 64)
		if err != nil {
			continue
		}
		es = append(es, db.MissedEntry{Connection: conn, Channel: channel, After: after})
	}
	return es
}

func (a *api) webPushKeys(w http.ResponseWriter, r *http.Request, user string) {
	var key *string
	if a.cfg.VAPIDPublic != "" {
		key = &a.cfg.VAPIDPublic
	}
	respond.JSON(w, http.StatusOK, struct {
		Key *string `json:"vapid_public_key"`
	}{key})
}

// channels lists the channels the user's connection id has logs in.
func (a *api) channels(w http.ResponseWriter, r *http.Request, user, id string) {
	names, err := a.db.ChannelNames(r.Context(), id)
	list(w, "channels", names, err)
}
