package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/stream"
	"github.com/lierc/lierc-api/internal/testenv"
)

// deletesFirst is a Store whose SetSeen deletes the connection just before
// writing: a delete landing between the API's ownership check and the write.
type deletesFirst struct {
	db.Store
	d *db.DB
}

func (s deletesFirst) SetSeen(ctx context.Context, user, conn, channel string, id int32) error {
	if _, err := s.d.Pool.Exec(ctx, `DELETE FROM connections WHERE id = $1`, conn); err != nil {
		return err
	}
	return s.Store.SetSeen(ctx, user, conn, channel, id)
}

// A write that races its connection's delete answers as the ownership
// check would have: 401 Invalid connection id, not 500.
func TestSeenRacingDelete(t *testing.T) {
	cfg := config.Config{AccountTokenSecret: testSecret}
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", u, `{}`)
	lc := &liercd.Client{HTTP: &http.Client{}}
	srv := httptest.NewServer(New(cfg, deletesFirst{Store: d, d: d}, stream.New(cfg, d, lc), lc))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest("POST", srv.URL+"/connection/c1/channel/%23a/seen", strings.NewReader("5"))
	req.AddCookie(&http.Cookie{Name: "chats", Value: testenv.Login(t, d, u)})
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 401 || !strings.Contains(string(body), "Invalid connection id 'c1'") {
		t.Errorf("seen = %d %s, want 401 Invalid connection id", res.StatusCode, body)
	}
}
