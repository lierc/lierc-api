// Package mail sends emails over SMTP, configured from SMTP_OPTS: the API's,
// and lierc ingest's notifications.
package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"
)

const (
	DefaultHost    = "localhost"
	DefaultPort    = 25
	DefaultTimeout = 10 * time.Second
)

// Opts is the SMTP connection's settings.
type Opts struct {
	Host     string
	Port     int
	SSL      bool // implicit TLS
	AuthUser string
	AuthPass string
	// Timeout bounds connecting and each read and write.
	Timeout time.Duration
}

// ParseOpts reads a comma separated list of key value pairs, as found in
// the SMTP_OPTS env var:
//
//	Host,smtp.example.com,Port,465,SSL,1,AuthUser,me,AuthPass,secret,Timeout,10
//
// Keys are Host, Port, SSL, AuthUser, AuthPass and Timeout (seconds). SSL
// is true when not empty and not "0". Empty gives localhost:25 without TLS
// or auth, with a 10s timeout.
func ParseOpts(s string) (Opts, error) {
	o := Opts{Host: DefaultHost, Port: DefaultPort, Timeout: DefaultTimeout}

	s = strings.TrimSpace(s)
	if s == "" {
		return o, nil
	}

	pairs := strings.Split(s, ",")
	if len(pairs)%2 != 0 {
		return Opts{}, fmt.Errorf("SMTP opts has an odd number of values: %s", s)
	}

	for i := 0; i < len(pairs); i += 2 {
		key := strings.TrimSpace(pairs[i])
		value := strings.TrimSpace(pairs[i+1])

		switch strings.ToLower(key) {
		case "host":
			o.Host = value
		case "port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return Opts{}, fmt.Errorf("invalid SMTP port %s: %s", value, err)
			}
			o.Port = port
		case "ssl":
			o.SSL = value != "" && value != "0"
		case "authuser":
			o.AuthUser = value
		case "authpass":
			o.AuthPass = value
		case "timeout":
			secs, err := strconv.ParseFloat(value, 64)
			if err != nil || secs <= 0 {
				return Opts{}, fmt.Errorf("invalid SMTP timeout %s: want a positive number of seconds", value)
			}
			o.Timeout = time.Duration(secs * float64(time.Second))
		default:
			return Opts{}, fmt.Errorf("unknown SMTP opt %s", key)
		}
	}
	return o, nil
}

// Sender sends plain text emails.
type Sender struct {
	Opts Opts
	From string
}

// Send sends one email.
func (s *Sender) Send(to, subject, body string) error {
	if strings.ContainsAny(to+subject+s.From, "\r\n") {
		return fmt.Errorf("mail: newline in a header value")
	}
	m := gomail.NewMsg()
	if err := m.From(s.From); err != nil {
		return err
	}
	if err := m.To(to); err != nil {
		return err
	}
	m.Subject(subject)
	m.SetUserAgent("relaychat")
	m.SetBodyString(gomail.TypeTextPlain, body)
	return s.Opts.Send(m)
}

// Send sends m, built by the caller, as o says (see client).
func (o Opts) Send(m *gomail.Msg) error {
	c, err := client(o)
	if err != nil {
		return err
	}
	return c.DialAndSend(m)
}

// client connects as Opts says: implicit TLS with SSL, otherwise STARTTLS
// when the server offers it, and PLAIN auth when AuthUser is set (only
// over TLS, unless the server is localhost).
func client(o Opts) (*gomail.Client, error) {
	host, port, timeout := o.Host, o.Port, o.Timeout
	if host == "" {
		host = DefaultHost
	}
	if port == 0 {
		port = DefaultPort
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	opts := []gomail.Option{
		gomail.WithPort(port),
		gomail.WithTimeout(timeout),
		gomail.WithDialContextFunc(dialer(host, o.SSL, timeout)),
		gomail.WithSMTPAuth(gomail.SMTPAuthNoAuth),
	}
	if o.SSL {
		// dialer does the TLS; this keeps go-mail from trying STARTTLS
		opts = append(opts, gomail.WithSSL())
	} else {
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSOpportunistic))
	}
	if o.AuthUser != "" {
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthPlain),
			gomail.WithUsername(o.AuthUser), gomail.WithPassword(o.AuthPass))
	}
	return gomail.NewClient(host, opts...)
}

// dialer connects with a deadline on every read and write, so a server
// that stops answering fails the send, while a slow but working one
// doesn't (go-mail's own timeout is for the whole session). With ssl it
// does the TLS handshake, which go-mail leaves to a custom dialer.
func dialer(host string, ssl bool, timeout time.Duration) gomail.DialContextFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		conn := &deadlineConn{Conn: raw, timeout: timeout}
		if !ssl {
			return conn, nil
		}
		tc := tls.Client(conn, &tls.Config{ServerName: host})
		if err := tc.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, err
		}
		return tc, nil
	}
}

// deadlineConn gives every read and write its own deadline, replacing the
// one go-mail sets for the session.
type deadlineConn struct {
	net.Conn
	timeout time.Duration
}

func (c *deadlineConn) Read(b []byte) (int, error) {
	c.Conn.SetDeadline(time.Now().Add(c.timeout))
	return c.Conn.Read(b)
}

func (c *deadlineConn) Write(b []byte) (int, error) {
	c.Conn.SetDeadline(time.Now().Add(c.timeout))
	return c.Conn.Write(b)
}
