package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/respond"
)

// The history reads, ported from lib/API/Controller/{Message,Channel}.pm.
func (a *api) history(mux *http.ServeMux) {
	mux.Handle("GET /log/{event}", a.users.Require(a.logSince))
	mux.Handle("GET /highlight", a.users.Require(a.highlights))
	mux.Handle("GET /highlight/{event}", a.users.Require(a.highlights))
	mux.Handle("GET /connection/{id}/channel/{channel}/events", a.users.Require(a.owned(a.channelLogs)))
	mux.Handle("GET /connection/{id}/channel/{channel}/events/{event}", a.users.Require(a.owned(a.channelLogs)))
	mux.Handle("GET /connection/{id}/channel/{channel}/last", a.users.Require(a.owned(a.channelLast)))
}

// limit is q's "limit": def when missing, not a number or below 1, and at
// most max.
func limit(q url.Values, def, max int) int {
	n, err := strconv.Atoi(q.Get("limit"))
	if err != nil || n < 1 {
		n = def
	}
	return min(n, max)
}

// eventID is the {event} path value as a message id. Otherwise it answers
// 400 and reports false.
func eventID(w http.ResponseWriter, r *http.Request) (int32, bool) {
	n, err := strconv.ParseInt(r.PathValue("event"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "event id must be a number")
		return 0, false
	}
	return int32(n), true
}

// channel is the {channel} path value, lowercased as Perl's lc did.
func channel(r *http.Request) string {
	return strings.ToLower(pathText(r, "channel"))
}

// logSince is what a reconnecting stream missed: every message above
// {event}, refused when there are too many to send.
func (a *api) logSince(w http.ResponseWriter, r *http.Request, user string) {
	after, ok := eventID(w, r)
	if !ok {
		return
	}
	ms, err := a.db.LogSince(r.Context(), user, after)
	if err != nil {
		respond.Internal(w, "log", err)
		return
	}
	if len(ms) >= db.LogMax {
		respond.Error(w, http.StatusBadRequest, "Too many messages!")
		return
	}
	respond.JSON(w, http.StatusOK, ms)
}

func (a *api) highlights(w http.ResponseWriter, r *http.Request, user string) {
	var before *int32
	if r.PathValue("event") != "" {
		id, ok := eventID(w, r)
		if !ok {
			return
		}
		before = &id
	}
	ms, err := a.db.Highlights(r.Context(), user, before, limit(r.URL.Query(), 50, 150))
	list(w, "highlight", ms, err)
}

// channelLogs is scrollback: the newest messages, or those below {event}.
func (a *api) channelLogs(w http.ResponseWriter, r *http.Request, user, id string) {
	n := limit(r.URL.Query(), 50, 150)
	if r.PathValue("event") == "" {
		ms, err := a.db.ChannelLogs(r.Context(), id, channel(r), n)
		list(w, "channel logs", ms, err)
		return
	}
	before, ok := eventID(w, r)
	if !ok {
		return
	}
	ms, err := a.db.ChannelLogsBefore(r.Context(), id, channel(r), before, n)
	list(w, "channel logs", ms, err)
}

func (a *api) channelLast(w http.ResponseWriter, r *http.Request, user, id string) {
	var query *string
	if q := r.URL.Query(); q.Has("query") {
		s := strings.ToValidUTF8(q.Get("query"), "�")
		query = &s
	}
	ms, err := a.db.ChannelLast(r.Context(), id, channel(r), query, limit(r.URL.Query(), 5, 20))
	list(w, "last", ms, err)
}
