package stream

import "net/http"

type streamInfo struct {
	Remote  string  `json:"remote"`
	Agent   *string `json:"agent"`
	Created int64   `json:"created"`
}

// serveStats answers /stats (bin/stream.psgi, Events.pm stats): stream counts
// per user, or one user's stream details with ?user=.
func (s *Server) serveStats(w http.ResponseWriter, r *http.Request) {
	key, ok := r.Header["Lierc-Key"]
	if !ok || key[0] != s.cfg.Key {
		writeError(w, http.StatusBadRequest, "Invalid key")
		return
	}

	writeJSON(w, http.StatusOK, s.stats(r.URL.Query().Get("user")))
}

// stats snapshots the registry; the response is written after the lock is released.
func (s *Server) stats(user string) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if user != "" && user != "0" { // Perl: if ($user)
		data := []streamInfo{}
		for _, st := range s.streams[user] {
			data = append(data, streamInfo{st.remote, st.agent, st.created})
		}
		return data
	}
	data := map[string]int{}
	for user, streams := range s.streams {
		if len(streams) > 0 {
			data[user] = len(streams)
		}
	}
	return data
}
