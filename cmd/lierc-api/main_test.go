package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func TestUnknownSubcommand(t *testing.T) {
	for _, args := range [][]string{{"bogus"}, {}, {"api"}, {"stream", "extra"}} {
		var buf bytes.Buffer
		if code := run(context.Background(), args, func(string) string { return "" }, &buf, nil); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
		if !strings.Contains(buf.String(), "usage:") {
			t.Errorf("%v: no usage in %q", args, buf.String())
		}
	}
}

func TestServesStreamUnauthorized(t *testing.T) {
	d := testenv.Postgres(t)
	nsqAddr, _ := testenv.NSQD(t)
	// The stream always opens database "lierc", which testenv does not
	// create; the pool connects lazily and a request without a cookie never
	// queries it.
	cc := d.Pool.Config().ConnConfig
	env := map[string]string{
		"POSTGRES_HOST":     net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port))),
		"POSTGRES_USER":     cc.User,
		"POSTGRES_PASSWORD": cc.Password,
		"LISTEN":            "127.0.0.1:0",
		"ADMIN_LISTEN":      "127.0.0.1:0",
		"NSQD_HOST":         nsqAddr,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := make(chan net.Addr, 1)
	done := make(chan int, 1)
	var logs bytes.Buffer
	go func() {
		done <- run(ctx, []string{"stream"}, func(k string) string { return env[k] }, &logs, func(a, _ net.Addr) { addr <- a })
	}()

	var a net.Addr
	select {
	case a = <-addr:
	case code := <-done:
		t.Fatalf("exited %d early: %s", code, logs.String())
	case <-time.After(30 * time.Second):
		t.Fatal("server did not start")
	}
	resp, err := http.Get("http://" + a.String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit %d: %s", code, logs.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no shutdown")
	}
}

// The server starts and serves without nsqd, and still shuts down cleanly.
func TestStreamServesWithoutNSQ(t *testing.T) {
	env := map[string]string{
		"POSTGRES_HOST": "127.0.0.1:1", // never queried without a cookie
		"LISTEN":        "127.0.0.1:0",
		"ADMIN_LISTEN":  "127.0.0.1:0",
		"NSQD_HOST":     "127.0.0.2:1", // nothing listens here
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := make(chan net.Addr, 1)
	done := make(chan int, 1)
	var logs syncBuffer
	go func() {
		done <- run(ctx, []string{"stream"}, func(k string) string { return env[k] }, &logs, func(a, _ net.Addr) { addr <- a })
	}()
	var a net.Addr
	select {
	case a = <-addr:
	case code := <-done:
		t.Fatalf("exited %d without nsqd: %s", code, logs.String())
	case <-time.After(30 * time.Second):
		t.Fatal("server did not start")
	}
	time.Sleep(200 * time.Millisecond) // the first connection attempt fails
	select {
	case code := <-done:
		t.Fatalf("exited %d without nsqd: %s", code, logs.String())
	default:
	}
	resp, err := http.Get("http://" + a.String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit %d: %s", code, logs.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no shutdown")
	}
}

// /stats is served, without a key, on the admin listener only.
func TestAdminListenerServesStats(t *testing.T) {
	env := map[string]string{
		"POSTGRES_HOST": "127.0.0.1:1", // never queried
		"LISTEN":        "127.0.0.1:0",
		"ADMIN_LISTEN":  "127.0.0.1:0",
		"NSQD_HOST":     "127.0.0.2:1",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addrs := make(chan [2]net.Addr, 1)
	done := make(chan int, 1)
	var logs syncBuffer
	go func() {
		done <- run(ctx, []string{"stream"}, func(k string) string { return env[k] }, &logs,
			func(s, a net.Addr) { addrs <- [2]net.Addr{s, a} })
	}()
	var a [2]net.Addr
	select {
	case a = <-addrs:
	case code := <-done:
		t.Fatalf("exited %d early: %s", code, logs.String())
	case <-time.After(30 * time.Second):
		t.Fatal("server did not start")
	}

	get := func(addr net.Addr) (int, string) {
		t.Helper()
		resp, err := http.Get("http://" + addr.String() + "/stats")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, body := get(a[1]); code != 200 || body != "{}" {
		t.Errorf("admin /stats = %d %q, want 200 {}", code, body)
	}
	if code, _ := get(a[0]); code != 401 {
		t.Errorf("stream /stats = %d, want 401", code)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit %d: %s", code, logs.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no shutdown")
	}
}

// A taken admin address fails startup.
func TestAdminListenInUse(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	env := map[string]string{
		"POSTGRES_HOST": "127.0.0.1:1",
		"LISTEN":        "127.0.0.1:0",
		"ADMIN_LISTEN":  taken.Addr().String(),
		"NSQD_HOST":     "127.0.0.2:1",
	}
	var logs syncBuffer
	if code := run(context.Background(), []string{"stream"}, func(k string) string { return env[k] }, &logs, nil); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(logs.String(), "admin listen") {
		t.Errorf("logs = %q", logs.String())
	}
}

// syncBuffer is a bytes.Buffer safe for the server's logger goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
