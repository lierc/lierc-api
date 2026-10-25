// Package liercd is the HTTP client for the liercd daemon, ported from
// lib/API/Liercd.pm and lib/API/Stream/Liercd.pm.
package liercd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/lierc/lierc-api/internal/db"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

type Response struct {
	Status int
	Body   []byte
}

// New returns a client for http://host:5005/ (Liercd.pm url()).
func New(host string) *Client {
	return &Client{
		BaseURL: fmt.Sprintf("http://%s:5005", host),
		HTTP:    &http.Client{Timeout: 180 * time.Second},
	}
}

// Do sends the request; path is relative to the base URL ("id/status").
func (c *Client) Do(ctx context.Context, method, path string, body []byte) (*Response, error) {
	url := c.BaseURL + "/" + trimSlash(path)
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/javascript")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	return &Response{Status: res.StatusCode, Body: b}, nil
}

func trimSlash(p string) string {
	for len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	return p
}

func statusLine(r *Response) string {
	return fmt.Sprintf("%d %s", r.Status, http.StatusText(r.Status))
}

// FindOrRecreate returns the connection's status body, re-creating the
// connection in liercd from the stored config when its status is not 200.
func (c *Client) FindOrRecreate(ctx context.Context, d *db.DB, id string) ([]byte, error) {
	config, ok, err := d.LookupConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("Connection does not exist")
	}

	res, err := c.Do(ctx, http.MethodGet, id+"/status", nil)
	if err != nil {
		return nil, err
	}
	if res.Status == http.StatusOK {
		return res.Body, nil
	}

	res, err = c.Do(ctx, http.MethodPost, id+"/create", config)
	if err != nil {
		return nil, err
	}
	if res.Status != http.StatusOK {
		return nil, errors.New("Unable to create new connection: " + statusLine(res))
	}

	res, err = c.Do(ctx, http.MethodGet, id+"/status", nil)
	if err != nil {
		return nil, err
	}
	if res.Status != http.StatusOK {
		return nil, errors.New("Unable to create new connection: " + statusLine(res))
	}
	return res.Body, nil
}
