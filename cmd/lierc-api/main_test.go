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

	"github.com/jackc/pgx/v5"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// lierc-api takes no arguments: it just runs the server. The old "stream"
// subcommand and "lctl" (now its own binary) are usage errors.
func TestArgumentsRefused(t *testing.T) {
	for _, args := range [][]string{{"bogus"}, {"stream"}, {"lctl", "help"}, {"stream", "extra"}} {
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
		"POSTGRES_HOST":        net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port))),
		"POSTGRES_USER":        cc.User,
		"POSTGRES_PASSWORD":    cc.Password,
		"LISTEN":               "127.0.0.1:0",
		"ADMIN_LISTEN":         "127.0.0.1:0",
		"ACCOUNT_TOKEN_SECRET": "acct",
		"UNSUBSCRIBE_SECRET":   "unsub",
		"NSQD_HOST":            nsqAddr,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := make(chan net.Addr, 1)
	done := make(chan int, 1)
	var logs bytes.Buffer
	go func() {
		done <- run(ctx, nil, func(k string) string { return env[k] }, &logs, func(a, _ net.Addr) { addr <- a })
	}()

	var a net.Addr
	select {
	case a = <-addr:
	case code := <-done:
		t.Fatalf("exited %d early: %s", code, logs.String())
	case <-time.After(30 * time.Second):
		t.Fatal("server did not start")
	}
	resp, err := http.Get("http://" + a.String() + "/events")
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
		"POSTGRES_HOST":        "127.0.0.1:1", // never queried without a cookie
		"LISTEN":               "127.0.0.1:0",
		"ADMIN_LISTEN":         "127.0.0.1:0",
		"ACCOUNT_TOKEN_SECRET": "acct",
		"UNSUBSCRIBE_SECRET":   "unsub",
		"NSQD_HOST":            "127.0.0.2:1", // nothing listens here
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := make(chan net.Addr, 1)
	done := make(chan int, 1)
	var logs syncBuffer
	go func() {
		done <- run(ctx, nil, func(k string) string { return env[k] }, &logs, func(a, _ net.Addr) { addr <- a })
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
	resp, err := http.Get("http://" + a.String() + "/events")
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
		"POSTGRES_HOST":        "127.0.0.1:1", // never queried
		"LISTEN":               "127.0.0.1:0",
		"ADMIN_LISTEN":         "127.0.0.1:0",
		"ACCOUNT_TOKEN_SECRET": "acct",
		"UNSUBSCRIBE_SECRET":   "unsub",
		"NSQD_HOST":            "127.0.0.2:1",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addrs := make(chan [2]net.Addr, 1)
	done := make(chan int, 1)
	var logs syncBuffer
	go func() {
		done <- run(ctx, nil, func(k string) string { return env[k] }, &logs,
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
	// The public listener has no /stats: an unknown path.
	if code, body := get(a[0]); code != 404 || body != `{"status":"error","error":"not found"}` {
		t.Errorf("public /stats = %d %q, want the 404", code, body)
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
		"POSTGRES_HOST":        "127.0.0.1:1",
		"LISTEN":               "127.0.0.1:0",
		"ADMIN_LISTEN":         taken.Addr().String(),
		"ACCOUNT_TOKEN_SECRET": "acct",
		"UNSUBSCRIBE_SECRET":   "unsub",
		"NSQD_HOST":            "127.0.0.2:1",
	}
	var logs syncBuffer
	if code := run(context.Background(), nil, func(k string) string { return env[k] }, &logs, nil); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(logs.String(), "admin listen") {
		t.Errorf("logs = %q", logs.String())
	}
}

// Without a signing secret the API doesn't start: it would otherwise sign
// verification, reset and unsubscribe links with an empty key.
func TestRefusesWithoutSecrets(t *testing.T) {
	for _, missing := range []string{"ACCOUNT_TOKEN_SECRET", "UNSUBSCRIBE_SECRET"} {
		env := map[string]string{
			"POSTGRES_HOST":        "127.0.0.1:1",
			"LISTEN":               "127.0.0.1:0",
			"ADMIN_LISTEN":         "127.0.0.1:0",
			"NSQD_HOST":            "127.0.0.2:1",
			"ACCOUNT_TOKEN_SECRET": "acct",
			"UNSUBSCRIBE_SECRET":   "unsub",
		}
		delete(env, missing)
		var logs syncBuffer
		if code := run(context.Background(), nil, func(k string) string { return env[k] }, &logs, nil); code != 1 {
			t.Errorf("without %s: exit %d, want 1", missing, code)
		}
		if !strings.Contains(logs.String(), missing+" is not set") {
			t.Errorf("without %s: logs = %q", missing, logs.String())
		}
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

// The admin JSON API is on the admin listener, beside /stats, and not on
// the public one.
func TestAdminRoutesMounted(t *testing.T) {
	d := testenv.Postgres(t)
	// run always opens database "lierc"; make one from the same template
	// the test databases come from.
	cc := d.Pool.Config().ConnConfig
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admin, err := pgx.Connect(ctx, "postgres://"+cc.User+":"+cc.Password+"@"+net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port)))+"/postgres?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if _, err := admin.Exec(ctx, "CREATE DATABASE lierc TEMPLATE lierc_template"); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP DATABASE IF EXISTS lierc WITH (FORCE)")

	env := map[string]string{
		"POSTGRES_HOST":        net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port))),
		"POSTGRES_USER":        cc.User,
		"POSTGRES_PASSWORD":    cc.Password,
		"LISTEN":               "127.0.0.1:0",
		"ADMIN_LISTEN":         "127.0.0.1:0",
		"ACCOUNT_TOKEN_SECRET": "acct",
		"UNSUBSCRIBE_SECRET":   "unsub",
		"NSQD_HOST":            "127.0.0.2:1",
	}
	addrs := make(chan [2]net.Addr, 1)
	done := make(chan int, 1)
	var logs syncBuffer
	go func() {
		done <- run(ctx, nil, func(k string) string { return env[k] }, &logs,
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
	status := func(addr net.Addr, path string) int {
		t.Helper()
		resp, err := http.Get("http://" + addr.String() + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := status(a[1], "/admin/log/last"); c != 200 {
		t.Errorf("admin /admin/log/last = %d, want 200", c)
	}
	if c := status(a[1], "/stats"); c != 200 {
		t.Errorf("admin /stats = %d, want 200", c)
	}
	if c := status(a[0], "/admin/log/last"); c != 404 {
		t.Errorf("public /admin/log/last = %d, want 404", c)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("no shutdown")
	}
}
