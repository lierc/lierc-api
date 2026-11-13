package api

import (
	"context"
	"errors"
	"io"
	"log"
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

// searchWriteTimeout is how long one write to a date search's client may
// block before the search gives up (and frees its database connection):
// there is no server-wide WriteTimeout, since streams must stay open.
var searchWriteTimeout = time.Minute

// channelDate runs the date search's query; tests replace it.
var channelDate = db.Store.ChannelDate

var dateShape = regexp.MustCompile(`^\d{4}-\d\d-\d\d$`)

// day is d as a strict YYYY-MM-DD real calendar date (Channel.pm _day).
func day(d string) (time.Time, bool) {
	if !dateShape.MatchString(d) {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", d)
	return t, err == nil
}

// sseEvent writes an event as Util::event does, with no id line.
func sseEvent(w io.Writer, typ string, data []byte) error {
	_, err := io.WriteString(w, "event: "+typ+"\ndata: "+string(data)+"\n\n")
	return err
}

// optional is q's value for name, nil when missing or empty.
func optional(r *http.Request, name string) *string {
	v := strings.ToValidUTF8(r.URL.Query().Get(name), "�")
	if v == "" {
		return nil
	}
	return &v
}

// dateSearch streams the channel's messages from {from} through {to}, as
// "log" events read as they come, filtered by the text and sender
// parameters (Channel.pm date). Problems are "search_error" events: the
// stream has started by then.
func (a *api) dateSearch(w http.ResponseWriter, r *http.Request, user, id string) {
	w.Header().Set("Content-Type", "text/event-stream;charset=utf-8")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	rc.Flush()

	searchError := func(msg string) {
		b, _ := respond.Marshal(struct {
			Error string `json:"error"`
		}{msg})
		sseEvent(w, "search_error", b)
	}
	fromS, toS := r.PathValue("from"), r.PathValue("to")
	from, ok1 := day(fromS)
	to, ok2 := day(toS)
	if !ok1 || !ok2 {
		searchError("Dates must be YYYY-MM-DD")
		return
	}
	if to.Sub(from) > 30*24*time.Hour {
		searchError("Search at most 31 days at a time")
		return
	}

	err := channelDate(a.db, r.Context(), id, channel(r), fromS, toS, optional(r, "text"), optional(r, "sender"), func(m db.Message) error {
		b, err := respond.Marshal(m)
		if err != nil {
			return err
		}
		rc.SetWriteDeadline(time.Now().Add(searchWriteTimeout))
		if err := sseEvent(w, "log", b); err != nil {
			return err
		}
		return rc.Flush()
	})
	if err != nil && r.Context().Err() == nil && !errors.Is(err, context.Canceled) {
		log.Printf("date search: %v", err)
		searchError("Search failed")
	}
}
