package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/testenv"
)

type miscEnv struct {
	*connEnv
	uploads func() []testenv.ImgurUpload
}

func setupMisc(t *testing.T, vapid string) *miscEnv {
	t.Helper()
	d := testenv.Postgres(t)
	id, _, err := d.AddUser(context.Background(), "alice", "alice@example.com", "x")
	if err != nil {
		t.Fatal(err)
	}
	base, uploads := testenv.FakeImgur(t)
	app := &App{Cfg: config.Config{Secret: authSecret, ImgurBaseURL: base, VapidPublic: vapid}, DB: d, Imgur: http.DefaultClient, AssetDir: "."}
	ae := &authEnv{t: t, db: d, id: id}
	ae.codec.Secret = authSecret
	ae.h = ae.codec.Middleware(newHandler(app, routes))
	return &miscEnv{connEnv: &connEnv{authEnv: ae}, uploads: uploads}
}

func TestPrefUpsertRawBody(t *testing.T) {
	e := setupMisc(t, "")
	rec := e.raw("POST", "/preference/theme", `"dark"`)
	if rec.Code != 204 {
		t.Fatalf("upsert: %d %s", rec.Code, rec.Body)
	}
	rec = e.get("/preference/theme")
	if rec.Body.String() != `{"name":"theme","value":"\"dark\""}` {
		t.Fatalf("show: %s", rec.Body)
	}
	e.raw("POST", "/preference/theme", `light`) // update path
	if got := e.get("/preference/theme").Body.String(); got != `{"name":"theme","value":"light"}` {
		t.Fatalf("after update: %s", got)
	}
}

func TestPrefShow404(t *testing.T) {
	e := setupMisc(t, "")
	rec := e.get("/preference/nope")
	if rec.Code != 404 || rec.Body.String() != `{"status":"error","error":"not found"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestPrefList(t *testing.T) {
	e := setupMisc(t, "")
	if got := e.get("/preference").Body.String(); got != `[]` {
		t.Fatalf("empty list = %s", got)
	}
	e.raw("POST", "/preference/a", "1")
	var rows []map[string]string
	json.Unmarshal(e.get("/preference").Body.Bytes(), &rows)
	if len(rows) != 1 || rows[0]["name"] != "a" || rows[0]["value"] != "1" {
		t.Fatalf("list = %v", rows)
	}
}

func TestUnsubscribe(t *testing.T) {
	e := setupMisc(t, "")
	e.raw("POST", "/preference/email", "true")
	if rec := e.get("/unsubscribe"); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := e.get("/preference/email").Body.String(); got != `{"name":"email","value":"false"}` {
		t.Fatalf("got %s", got)
	}
}

func TestWebPushKeys(t *testing.T) {
	e := setupMisc(t, "pubkey")
	if got := e.get("/notification/web_push_keys").Body.String(); got != `{"vapid_public_key":"pubkey"}` {
		t.Fatalf("got %s", got)
	}
	e = setupMisc(t, "")
	if got := e.get("/notification/web_push_keys").Body.String(); got != `{"vapid_public_key":null}` {
		t.Fatalf("unset: %s", got)
	}
}

func (e *miscEnv) pushForm(endpoint, auth, key string) *httptest.ResponseRecorder {
	f := url.Values{"endpoint": {endpoint}, "auth": {auth}, "key": {key}}
	return e.raw("POST", "/notification/web_push", f.Encode(), "Content-Type", "application/x-www-form-urlencoded")
}

func TestWebPushUpsertByEndpoint(t *testing.T) {
	e := setupMisc(t, "")
	if rec := e.pushForm("https://push.example/1", "a1", "k1"); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	e.pushForm("https://push.example/1", "a2", "k2")
	e.pushForm("https://push.example/2", "a3", "k3")
	var rows []map[string]string
	json.Unmarshal(e.get("/notification/web_push").Body.Bytes(), &rows)
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	for _, r := range rows {
		if r["endpoint"] == "https://push.example/1" && (r["auth"] != "a2" || r["key"] != "k2") {
			t.Fatalf("not updated: %v", r)
		}
	}
}

func TestWebPushDeleteRestOfPath(t *testing.T) {
	e := setupMisc(t, "")
	e.pushForm("https://push.example/a/b?c=d", "a", "k")
	e.pushForm("https://push.example/keep", "a", "k")
	if rec := e.raw("DELETE", "/notification/web_push/https://push.example/a/b%3Fc=d", ""); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var rows []map[string]string
	json.Unmarshal(e.get("/notification/web_push").Body.Bytes(), &rows)
	if len(rows) != 1 || rows[0]["endpoint"] != "https://push.example/keep" {
		t.Fatalf("rows = %v", rows)
	}
}

func (e *miscEnv) upload(field, filename, content string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	w, _ := mw.CreateFormFile(field, filename)
	w.Write([]byte(content))
	mw.Close()
	return e.raw("POST", "/image", buf.String(), "Content-Type", mw.FormDataContentType())
}

func TestImageUpload(t *testing.T) {
	e := setupMisc(t, "")
	rec := e.upload("whatever", "cat.png", "PNGDATA")
	if rec.Code != 200 || rec.Header().Get("X-Imgur") != "yes" || !strings.Contains(rec.Body.String(), `"deletehash":"hash-cat.png"`) {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	ups := e.uploads()
	if len(ups) != 1 || ups[0].Field != "whatever" || ups[0].Content != "PNGDATA" ||
		ups[0].Authorization != "Client-ID 033f98700d8577c" {
		t.Fatalf("uploads = %+v", ups)
	}
	var rows []map[string]string
	json.Unmarshal(e.get("/image").Body.Bytes(), &rows)
	if len(rows) != 1 || rows[0]["url"] != "https://i.imgur.com/cat.png" || rows[0]["created"] == "" {
		t.Fatalf("rows = %v", rows)
	}
}

func TestImageNoUpload(t *testing.T) {
	e := setupMisc(t, "")
	expectConnError(t, e.raw("POST", "/image", ""), "No upload")
}

func TestImageUploadImgurFails(t *testing.T) {
	e := setupMisc(t, "")
	rec := e.upload("f", "fail", "x")
	if rec.Code != 429 || rec.Body.String() != `{"success":false}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := e.get("/image").Body.String(); got != `[]` {
		t.Fatalf("saved: %s", got)
	}
}

func TestImageList(t *testing.T) {
	e := setupMisc(t, "")
	for i := 0; i < 60; i++ {
		_, err := e.db.Pool.Exec(context.Background(),
			`INSERT INTO image ("user", url, delete_hash, created) VALUES ($3, $1, 'h', now() + $2 * interval '1 second')`,
			"u"+string(rune('A'+i/26))+string(rune('a'+i%26)), i, e.id)
		if err != nil {
			t.Fatal(err)
		}
	}
	list := func(q string) []map[string]string {
		var rows []map[string]string
		json.Unmarshal(e.get("/image"+q).Body.Bytes(), &rows)
		return rows
	}
	if n := len(list("")); n != 15 {
		t.Fatalf("default = %d", n)
	}
	if n := len(list("?limit=500")); n != 50 {
		t.Fatalf("max = %d", n)
	}
	p0, p1 := list("?limit=10"), list("?limit=10&page=1")
	if p0[0]["url"] != "uCh" || p1[0]["url"] != "uBx" {
		t.Fatalf("order: %v / %v", p0[0], p1[0])
	}
}

func TestImageDelete(t *testing.T) {
	e := setupMisc(t, "")
	e.upload("f", "cat.png", "x")
	e.upload("f", "fail", "x") // 429, not saved
	e.db.Pool.Exec(context.Background(), `INSERT INTO image ("user", url, delete_hash) VALUES ($1,'https://i.imgur.com/fail','hash-fail')`, e.id)

	rec := e.raw("DELETE", "/image/https://nope.example/x.png", "")
	if rec.Code != 404 || rec.Body.String() != `{"status":"error","error":"not found"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = e.raw("DELETE", "/image/https://i.imgur.com/fail", "")
	if rec.Code != 403 {
		t.Fatalf("imgur failure not passed through: %d", rec.Code)
	}
	var n int
	e.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM image`).Scan(&n)
	if n != 2 {
		t.Fatalf("rows after failed delete = %d", n)
	}
	rec = e.raw("DELETE", "/image/https://i.imgur.com/cat.png", "")
	if rec.Code != 200 || rec.Body.String() != `{"success":true}` {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	e.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM image`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows after delete = %d", n)
	}
}
