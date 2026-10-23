package testenv

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/mail"
)

// FakeSMTP starts a plain SMTP server on 127.0.0.1 and returns client options
// for it and an accessor for the messages it has accepted.
func FakeSMTP(t testing.TB) (config.SMTPOpts, func() []mail.Received) {
	return FakeSMTPWith(t, false, "", "")
}

// FakeSMTPWith is FakeSMTP with implicit TLS (self-signed; the returned options
// carry SSL_verify_mode=0) and/or required AUTH PLAIN/LOGIN credentials.
func FakeSMTPWith(t testing.TB, useTLS bool, user, pass string) (config.SMTPOpts, func() []mail.Received) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if useTLS {
		cert := selfSigned(t)
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}})
	}
	s := &smtpServer{user: user, pass: pass}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	opts := config.SMTPOpts{
		Host:     "127.0.0.1",
		Port:     ln.Addr().(*net.TCPAddr).Port,
		SSL:      useTLS,
		AuthUser: user,
		AuthPass: pass,
		Extra:    map[string]string{},
	}
	if useTLS {
		opts.Extra["SSL_verify_mode"] = "0"
	}
	return opts, s.received
}

func selfSigned(t testing.TB) tls.Certificate {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

type smtpServer struct {
	user, pass string
	mu         sync.Mutex
	msgs       []mail.Received
}

func (s *smtpServer) received() []mail.Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mail.Received(nil), s.msgs...)
}

func (s *smtpServer) serve(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	r := bufio.NewReader(c)
	say := func(f string, a ...any) { fmt.Fprintf(c, f+"\r\n", a...) }
	line := func() (string, bool) {
		l, err := r.ReadString('\n')
		return strings.TrimRight(l, "\r\n"), err == nil
	}
	var cur mail.Received
	authed := s.user == ""
	say("220 fake ESMTP")
	for {
		l, ok := line()
		if !ok {
			return
		}
		verb, arg, _ := strings.Cut(l, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			cur.Hello = arg
			if s.user != "" {
				say("250-fake\r\n250 AUTH PLAIN LOGIN")
			} else {
				say("250 fake")
			}
		case "AUTH":
			mech, init, _ := strings.Cut(arg, " ")
			var gotUser, gotPass string
			switch strings.ToUpper(mech) {
			case "PLAIN":
				if init == "" {
					say("334 ")
					init, _ = line()
				}
				b, _ := base64.StdEncoding.DecodeString(init)
				p := strings.Split(string(b), "\x00")
				if len(p) == 3 {
					gotUser, gotPass = p[1], p[2]
				}
			case "LOGIN":
				say("334 %s", base64.StdEncoding.EncodeToString([]byte("Username:")))
				u, _ := line()
				say("334 %s", base64.StdEncoding.EncodeToString([]byte("Password:")))
				p, _ := line()
				ub, _ := base64.StdEncoding.DecodeString(u)
				pb, _ := base64.StdEncoding.DecodeString(p)
				gotUser, gotPass = string(ub), string(pb)
			default:
				say("504 unsupported")
				continue
			}
			if gotUser == s.user && gotPass == s.pass {
				authed = true
				cur.AuthUser = gotUser
				say("235 ok")
			} else {
				say("535 bad credentials")
			}
		case "MAIL":
			if !authed {
				say("530 auth required")
				continue
			}
			cur.From = strings.Trim(arg[len("FROM:"):], "<> ")
			say("250 ok")
		case "RCPT":
			cur.To = append(cur.To, strings.Trim(arg[len("TO:"):], "<> "))
			say("250 ok")
		case "DATA":
			say("354 go")
			var data strings.Builder
			for {
				l, ok := line()
				if !ok {
					return
				}
				if l == "." {
					break
				}
				data.WriteString(strings.TrimPrefix(l, ".") + "\r\n") // dot-unstuff
			}
			cur.Data = data.String()
			s.mu.Lock()
			s.msgs = append(s.msgs, cur)
			s.mu.Unlock()
			cur = mail.Received{Hello: cur.Hello, AuthUser: cur.AuthUser}
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}
