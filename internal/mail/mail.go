// Package mail sends the API's emails over SMTP, configured from SMTP_OPTS
// as the Perl API and the notifier read it.
package mail

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
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

// deadlineConn gives every read and write its own deadline, so a server
// that stops answering fails the send, while a slow but working one
// doesn't.
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

func (s *Sender) message(to, subject, body string) ([]byte, error) {
	if strings.ContainsAny(to+subject+s.From, "\r\n") {
		return nil, fmt.Errorf("mail: newline in a header value")
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", s.From)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("UTF-8", subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
	b.WriteString("\r\n")
	w := quotedprintable.NewWriter(&b)
	if _, err := w.Write([]byte(body)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Send sends one email.
func (s *Sender) Send(to, subject, body string) error {
	msg, err := s.message(to, subject, body)
	if err != nil {
		return err
	}

	o := s.Opts
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
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	raw, err := (&net.Dialer{Timeout: timeout}).Dial("tcp", addr)
	if err != nil {
		return err
	}
	var conn net.Conn = &deadlineConn{Conn: raw, timeout: timeout}
	tlsConfig := &tls.Config{ServerName: host}

	if o.SSL {
		tc := tls.Client(conn, tlsConfig)
		if err := tc.Handshake(); err != nil {
			raw.Close()
			return err
		}
		conn = tc
	}

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		raw.Close()
		return err
	}
	defer c.Close()

	// as smtp.SendMail does: upgrade a plain connection when offered
	if !o.SSL {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsConfig); err != nil {
				return err
			}
		}
	}

	if o.AuthUser != "" {
		if err := c.Auth(smtp.PlainAuth("", o.AuthUser, o.AuthPass, host)); err != nil {
			return err
		}
	}

	if err := c.Mail(s.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
