package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/respond"
)

func (a *api) search(mux *http.ServeMux) {
	mux.Handle("GET /connection/{id}/channel/{channel}/date/{from}/{to}", a.users.Require(a.owned(a.dateSearch)))
}

// dateLimit is the most messages a date search sends: the oldest, with
// truncated set when there were more. (31 days of a busy channel can be
// tens of thousands, more than a page can show.)
var dateLimit = 5000

// dateWriteTimeout is how long writing a date search's response may take
// before it's abandoned: there is no server-wide WriteTimeout, since
// streams must stay open.
var dateWriteTimeout = time.Minute

// dateWritten is called when a date search's response is written or
// abandoned; tests replace it.
var dateWritten = func() {}

// channelDate runs the date search's query; tests replace it.
var channelDate = db.Store.ChannelDate

var dateShape = regexp.MustCompile(`^\d{4}-\d\d-\d\d$`)

// day is d as a strict YYYY-MM-DD real calendar date.
func day(d string) (time.Time, bool) {
	if !dateShape.MatchString(d) {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", d)
	return t, err == nil
}

// optional is q's value for name, nil when missing or empty.
func optional(r *http.Request, name string) *string {
	v := strings.ToValidUTF8(r.URL.Query().Get(name), "�")
	if v == "" {
		return nil
	}
	return &v
}

// dateSearch answers the channel's messages from {from} through {to},
// days in the time zone the tz parameter names (UTC without it),
// filtered by the text and sender parameters, as {"messages": [...],
// "truncated": bool}.
func (a *api) dateSearch(w http.ResponseWriter, r *http.Request, user, id string) {
	fromS, toS := r.PathValue("from"), r.PathValue("to")
	from, ok1 := day(fromS)
	to, ok2 := day(toS)
	if !ok1 || !ok2 {
		respond.Error(w, http.StatusBadRequest, "Dates must be YYYY-MM-DD")
		return
	}
	if to.Sub(from) > 30*24*time.Hour {
		respond.Error(w, http.StatusBadRequest, "Search at most 31 days at a time")
		return
	}
	// the zone the dates are days in: the browser's, UTC for older pages
	tz := "UTC"
	if z := optional(r, "tz"); z != nil {
		tz = *z
	}
	ms, err := channelDate(a.db, r.Context(), id, channel(r), fromS, toS, tz, optional(r, "text"), optional(r, "sender"), dateLimit+1)
	if errors.Is(err, db.ErrUnknownTimeZone) {
		respond.Error(w, http.StatusBadRequest, fmt.Sprintf("Unknown time zone '%s'", tz))
		return
	}
	if err != nil {
		if r.Context().Err() == nil && !errors.Is(err, context.Canceled) {
			respond.Internal(w, "date search", err)
		}
		return
	}
	truncated := len(ms) > dateLimit
	if truncated {
		ms = ms[:dateLimit]
	}
	if ms == nil {
		ms = []db.Message{}
	}
	defer dateWritten()
	http.NewResponseController(w).SetWriteDeadline(time.Now().Add(dateWriteTimeout))
	respond.JSON(w, http.StatusOK, struct {
		Messages  []db.Message `json:"messages"`
		Truncated bool         `json:"truncated"`
	}{ms, truncated})
}
