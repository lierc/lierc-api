// Package mail sends the service's email.
package mail

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lierc/lierc-api/internal/config"
)

// Mailer sends a plain-text email.
type Mailer interface {
	Send(to, subject, body string) error
}

// Received is one message accepted by a test SMTP server (testenv.FakeSMTP).
type Received struct {
	Hello    string
	AuthUser string
	From     string
	To       []string
	// Data is the message as sent over the wire after dot-unstuffing.
	Data string
}

// New returns a Mailer that delivers through SMTP with opts, sending from
// from. When disabled (LIERC_NO_SMTP) Send does nothing.
func New(opts config.SMTPOpts, from string, disabled bool) Mailer {
	if disabled {
		return nopMailer{}
	}
	return &smtpMailer{opts: opts, from: from}
}

type nopMailer struct{}

func (nopMailer) Send(string, string, string) error { return nil }

type smtpMailer struct {
	opts config.SMTPOpts
	from string
}

// timeout is Net::SMTP's default Timeout.
const timeout = 120 * time.Second

// Send mirrors send_smtp in lib/API/Controller/Auth.pm: connect, AUTH when
// AuthUser is set, MAIL FROM, RCPT TO, DATA, QUIT.
func (m *smtpMailer) Send(to, subject, body string) error {
	o := m.opts
	// Net::SMTP->new with no Host falls back to "localhost".
	host := o.Host
	if host == "" {
		host = "localhost"
	}
	port := o.Port
	if port == 0 {
		port = 25
		if o.SSL {
			port = 465 // Net::SMTP's default port for SSL
		}
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	d := net.Dialer{Timeout: timeout}
	var conn net.Conn
	var err error
	if o.SSL {
		// SSL_verify_mode => 0 is the only IO::Socket::SSL option honoured.
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: o.Extra["SSL_verify_mode"] == "0",
		})
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("SMTP connect failed: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("SMTP connect failed: %v", err)
	}
	hello := o.Hello
	if hello == "" {
		if hello, _ = os.Hostname(); hello == "" {
			hello = "localhost"
		}
	}
	if err := c.Hello(hello); err != nil {
		return fmt.Errorf("SMTP connect failed: %v", err)
	}

	if o.AuthUser != "" {
		if err := c.Auth(chooseAuth(c, o.AuthUser, o.AuthPass)); err != nil {
			return fmt.Errorf("SMTP AUTH failed: %v", err)
		}
	}
	if err := c.Mail(m.from); err != nil {
		return fmt.Errorf("SMTP MAIL FROM failed: %v", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP RCPT TO failed: %v", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA failed: %v", err)
	}
	if _, err := w.Write(Message(m.from, to, subject, body)); err != nil {
		return fmt.Errorf("SMTP data write failed: %v", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP DATAEND failed: %v", err)
	}
	c.Quit()
	return nil
}

// Message renders what MIME::Lite->new(Type => 'TEXT', Encoding =>
// 'quoted-printable') produces: headers, then a quoted-printable body.
func Message(from, to, subject, body string) []byte {
	charset := "us-ascii" // MIME::Lite's default
	for i := 0; i < len(body); i++ {
		if body[i] >= 0x80 {
			charset = "utf-8"
			break
		}
	}
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("Date", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	h("From", from)
	h("To", to)
	h("Subject", subject)
	h("Content-Type", `text/plain; charset="`+charset+`"`)
	h("Content-Transfer-Encoding", "quoted-printable")
	h("MIME-Version", "1.0")
	b.WriteString("\r\n")
	w := quotedprintable.NewWriter(&b)
	w.Write([]byte(body))
	w.Close()
	b.WriteString("\r\n")
	return b.Bytes()
}

// chooseAuth picks the strongest mechanism the server offers, as
// Authen::SASL does for Net::SMTP. Unlike net/smtp's PlainAuth these do not
// refuse an unencrypted connection, which Net::SMTP permits.
func chooseAuth(c *smtp.Client, user, pass string) smtp.Auth {
	_, mechs := c.Extension("AUTH")
	has := func(m string) bool {
		for _, f := range strings.Fields(strings.ToUpper(mechs)) {
			if f == m {
				return true
			}
		}
		return false
	}
	switch {
	case has("CRAM-MD5"):
		return smtp.CRAMMD5Auth(user, pass)
	case has("PLAIN"):
		return plainAuth{user, pass}
	default:
		return loginAuth{user, pass}
	}
}

type plainAuth struct{ user, pass string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}

func (plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected challenge")
	}
	return nil, nil
}

type loginAuth struct{ user, pass string }

func (a loginAuth) Start(*smtp.ServerInfo) (string, []byte, error) { return "LOGIN", nil, nil }

func (a loginAuth) Next(challenge []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(challenge))) {
	case "username:":
		return []byte(a.user), nil
	case "password:":
		return []byte(a.pass), nil
	}
	return nil, fmt.Errorf("unexpected challenge %q", challenge)
}
