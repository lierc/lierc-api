package mail_test

import (
	"io"
	"mime/quotedprintable"
	"net"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/mail"
	"github.com/lierc/lierc-api/internal/testenv"
)

func send(t *testing.T, opts config.SMTPOpts, msgs func() []mail.Received, body string) mail.Received {
	t.Helper()
	m := mail.New(opts, "from@example.com", false)
	if err := m.Send("to@example.com", "Hi there", body); err != nil {
		t.Fatal(err)
	}
	got := msgs()
	if len(got) != 1 {
		t.Fatalf("got %d messages", len(got))
	}
	return got[0]
}

func TestMailPlain(t *testing.T) {
	opts, msgs := testenv.FakeSMTP(t)
	opts.Hello = "hello.example"
	body := "line one\n.dot line\n\nhéllo = done"
	r := send(t, opts, msgs, body)
	if r.Hello != "hello.example" {
		t.Errorf("EHLO %q", r.Hello)
	}
	if r.From != "from@example.com" || len(r.To) != 1 || r.To[0] != "to@example.com" {
		t.Errorf("envelope %+v", r)
	}
	head, enc, ok := strings.Cut(r.Data, "\r\n\r\n")
	if !ok {
		t.Fatalf("no header/body split: %q", r.Data)
	}
	for _, want := range []string{"From: from@example.com", "To: to@example.com", "Subject: Hi there",
		"Content-Transfer-Encoding: quoted-printable", `charset="utf-8"`} {
		if !strings.Contains(head, want) {
			t.Errorf("header missing %q in %q", want, head)
		}
	}
	dec, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(enc)))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ReplaceAll(strings.TrimSuffix(string(dec), "\r\n"), "\r\n", "\n"); got != body {
		t.Errorf("body = %q, want %q", got, body)
	}
	if strings.Contains(enc, "é") || !strings.Contains(enc, "=3D") {
		t.Errorf("body not quoted-printable: %q", enc)
	}
}

func TestMailImplicitTLS(t *testing.T) {
	opts, msgs := testenv.FakeSMTPWith(t, true, "", "")
	send(t, opts, msgs, "x")
}

func TestMailAuth(t *testing.T) {
	opts, msgs := testenv.FakeSMTPWith(t, false, "bob", "s3cret")
	if r := send(t, opts, msgs, "x"); r.AuthUser != "bob" {
		t.Errorf("AuthUser = %q", r.AuthUser)
	}
	opts.AuthPass = "wrong"
	err := mail.New(opts, "a@b.c", false).Send("d@e.f", "s", "b")
	if err == nil || !strings.Contains(err.Error(), "SMTP AUTH failed") {
		t.Errorf("err = %v", err)
	}
	opts.AuthUser = ""
	err = mail.New(opts, "a@b.c", false).Send("d@e.f", "s", "b")
	if err == nil || !strings.Contains(err.Error(), "SMTP MAIL FROM failed") {
		t.Errorf("no-auth err = %v", err)
	}
}

func TestMailEmptyHostIsLocalhost(t *testing.T) {
	opts, msgs := testenv.FakeSMTP(t)
	if _, err := net.LookupHost("localhost"); err != nil {
		t.Skip("no localhost")
	}
	opts.Host = ""
	send(t, opts, msgs, "x")
}

func TestMailDisabled(t *testing.T) {
	opts, msgs := testenv.FakeSMTP(t)
	if err := mail.New(opts, "a@b.c", true).Send("d@e.f", "s", "b"); err != nil {
		t.Fatal(err)
	}
	if n := len(msgs()); n != 0 {
		t.Errorf("%d messages sent while disabled", n)
	}
}

func TestMailConnectFailure(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	err := mail.New(config.SMTPOpts{Host: "127.0.0.1", Port: port}, "a@b.c", false).Send("d@e.f", "s", "b")
	if err == nil || !strings.HasPrefix(err.Error(), "SMTP connect failed") {
		t.Errorf("err = %v", err)
	}
}
