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
	"strings"
	"time"

	"github.com/lierc/lierc-api/internal/db"
)

type Client struct {
	// BaseURL is the main instance's base URL, and the only one without
	// routing.
	BaseURL string
	HTTP    *http.Client

	// nextURL and instanceOf are set by NewRouted: instanceOf names the
	// instance a connection id is on ("" for main, "next").
	nextURL    string
	instanceOf func(ctx context.Context, id string) (string, error)
}

// Instances are the liercd hosts: Main (LIERCD_HOST) and Next
// (LIERCD_NEXT_HOST, "" when unset).
type Instances struct{ Main, Next string }

type Response struct {
	Status int
	Body   []byte
}

// New returns a client for http://host:5005/ (Liercd.pm url()).
func New(host string) *Client {
	return &Client{
		BaseURL: baseURL(host),
		HTTP:    &http.Client{Timeout: 180 * time.Second},
	}
}

// NewRouted returns a client that sends each request to the instance
// instanceOf names for the path's connection id.
func NewRouted(hosts Instances, instanceOf func(ctx context.Context, id string) (string, error)) *Client {
	next := ""
	if hosts.Next != "" {
		next = baseURL(hosts.Next)
	}
	return newRoutedBase(baseURL(hosts.Main), next, instanceOf)
}

func newRoutedBase(mainBase, nextBase string, instanceOf func(ctx context.Context, id string) (string, error)) *Client {
	c := New("")
	c.BaseURL, c.nextURL, c.instanceOf = mainBase, nextBase, instanceOf
	return c
}

func baseURL(host string) string { return fmt.Sprintf("http://%s:5005", host) }

// base returns the base URL for path: the instance of the connection id
// that starts it, or main without routing.
func (c *Client) base(ctx context.Context, path string) (string, error) {
	if c.instanceOf == nil {
		return c.BaseURL, nil
	}
	id, _, _ := strings.Cut(path, "/")
	if id == "" {
		return c.BaseURL, nil
	}
	inst, err := c.instanceOf(ctx, id)
	if err != nil {
		return "", err
	}
	switch inst {
	case "":
		return c.BaseURL, nil
	case "next":
		if c.nextURL == "" {
			return "", errors.New(`liercd instance "next" has no host (LIERCD_NEXT_HOST)`)
		}
		return c.nextURL, nil
	}
	return "", fmt.Errorf("unknown liercd instance %q", inst)
}

// For returns a client pinned to one instance ("main" or "next"), for calls
// that must reach that instance whatever the connection's column says (a
// move, which changes the column). It errors for "next" without a host.
func (c *Client) For(instance string) (*Client, error) {
	base := c.BaseURL
	switch instance {
	case "main":
	case "next":
		if c.nextURL == "" {
			return nil, errors.New(`liercd instance "next" has no host (LIERCD_NEXT_HOST)`)
		}
		base = c.nextURL
	default:
		return nil, fmt.Errorf("unknown liercd instance %q", instance)
	}
	return &Client{BaseURL: base, HTTP: c.HTTP}, nil
}

// Do sends the request; path is relative to the base URL ("id/status").
func (c *Client) Do(ctx context.Context, method, path string, body []byte) (*Response, error) {
	path = trimSlash(path)
	base, err := c.base(ctx, path)
	if err != nil {
		return nil, err
	}
	url := base + "/" + path
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
