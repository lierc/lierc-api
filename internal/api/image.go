package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
)

// Handlers ported from lib/API/Controller/Image.pm.

const imgurAuth = "Client-ID 033f98700d8577c"

// imgurDo sends req to Imgur and returns the passthrough response, its status
// and body.
func (a *App) imgurDo(req *http.Request) (Response, int, []byte, error) {
	req.Header.Set("Authorization", imgurAuth)
	res, err := a.Imgur.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, 0, nil, err
	}
	return Passthrough(res.StatusCode, res.Header, body), res.StatusCode, body, nil
}

func imageDelete(a *App, r *Request) (Response, error) {
	user, url := r.Session.User(), r.Params["url"]
	var del string
	rows, err := a.DB.Pool.Query(r.Context(), `
		SELECT delete_hash FROM image
		WHERE "user"=$1 AND url=$2`, user, url)
	if err != nil {
		return nil, err
	}
	found := rows.Next()
	if found {
		err = rows.Scan(&del)
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Perl tests the hash for truth.
	if !found || del == "" || del == "0" {
		return NotFound(), nil
	}

	req, err := http.NewRequestWithContext(r.Context(), "DELETE", a.Cfg.ImgurBaseURL+"/3/image/"+del, nil)
	if err != nil {
		return nil, err
	}
	resp, code, _, err := a.imgurDo(req)
	if err != nil {
		return nil, err
	}
	if code == 200 {
		if _, err := a.DB.Pool.Exec(r.Context(), `
			DELETE FROM image
			WHERE "user"=$1 AND url=$2`, user, url); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func imageList(a *App, r *Request) (Response, error) {
	limit := limitParam(r, 15, 50)
	// `$page * $limit` (Image.pm list) is computed numerically and bound in
	// Perl's number form: "abc" is 0, "1.5" gives a fractional OFFSET that
	// Postgres rejects.
	page := perlNumber{isInt: true}
	if v, _ := r.Param("page"); v != "" && v != "0" { // `|| 0`
		page = perlNum(v)
	}
	offset := perlMul(page, limitNum(limit)).String()
	rows, err := a.DB.Pool.Query(r.Context(), `
		SELECT url, created::text
		FROM image
		WHERE "user"=$1
		ORDER BY created DESC
		LIMIT $2 OFFSET $3`, r.Session.User(), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var url, created string
		if err := rows.Scan(&url, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"url": url, "created": created})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return JSON(out), nil
}

func imageCreate(a *App, r *Request) (Response, error) {
	ct, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	n := 0
	if strings.HasPrefix(ct, "multipart/") && params["boundary"] != "" {
		mr := multipart.NewReader(bytes.NewReader(r.Body), params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FileName() == "" { // only uploads, not plain fields
				continue
			}
			w, err := mw.CreatePart(p.Header)
			if err != nil {
				return nil, err
			}
			if _, err := io.Copy(w, p); err != nil {
				return nil, err
			}
			n++
		}
	}
	if n == 0 {
		return Error("No upload"), nil
	}
	mw.Close()

	req, err := http.NewRequestWithContext(r.Context(), "POST", a.Cfg.ImgurBaseURL+"/3/upload", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, code, body, err := a.imgurDo(req)
	if err != nil {
		return nil, err
	}
	if code == 200 {
		var data struct {
			Data struct {
				Link       *string `json:"link"`
				Deletehash *string `json:"deletehash"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, err
		}
		if _, err := a.DB.Pool.Exec(r.Context(), `
			INSERT INTO image ("user", url, delete_hash)
			VALUES ($1,$2,$3)`, r.Session.User(), data.Data.Link, data.Data.Deletehash); err != nil {
			return nil, err
		}
	}
	return resp, nil
}
