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

	s.mu.Lock()
	defer s.mu.Unlock()
	if user := r.URL.Query().Get("user"); user != "" && user != "0" { // Perl: if ($user)
		data := []streamInfo{}
		for _, st := range s.streams[user] {
			data = append(data, streamInfo{st.remote, st.agent, st.created})
		}
		writeJSON(w, http.StatusOK, data)
		return
	}
	data := map[string]int{}
	for user, streams := range s.streams {
		if len(streams) > 0 {
			data[user] = len(streams)
		}
	}
	writeJSON(w, http.StatusOK, data)
}
