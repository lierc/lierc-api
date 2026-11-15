package mail

import (
	"bufio"
	"io"
	"mime/quotedprintable"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseOpts(t *testing.T) {
	o, err := ParseOpts("")
	if err != nil {
		t.Fatal(err)
	}
	if o.Host != "localhost" || o.Port != 25 || o.Timeout != 10*time.Second || o.SSL || o.AuthUser != "" {
		t.Errorf("default = %+v", o)
	}

	o, err = ParseOpts("Host,h,Port,465,SSL,1,AuthUser,u,AuthPass,p,Timeout,3")
	if err != nil {
		t.Fatal(err)
	}
	want := Opts{Host: "h", Port: 465, SSL: true, AuthUser: "u", AuthPass: "p", Timeout: 3 * time.Second}
	if o != want {
		t.Errorf("got %+v, want %+v", o, want)
	}

	o, err = ParseOpts("SSL,0")
	if err != nil || o.SSL {
		t.Errorf("SSL,0 = %+v, %v", o, err)
	}

	for _, bad := range []string{"Host", "Host,a,Port", "Bogus,1", "Port,x", "Timeout,0", "Timeout,x"} {
		if _, err := ParseOpts(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

type got struct {
	from, rcpt, data string
}

// fakeSMTP serves one SMTP session and reports what it received.
func fakeSMTP(t *testing.T) (port int, res chan got) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	res = make(chan got, 1)
	go func() {
		defer l.Close()
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		tp := textproto.NewConn(c)
		var g got
		tp.PrintfLine("220 fake ESMTP")
		for {
			line, err := tp.ReadLine()
			if err != nil {
				return
			}
			cmd := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				tp.PrintfLine("250 fake")
			case strings.HasPrefix(cmd, "MAIL FROM:"):
				g.from = line[len("MAIL FROM:"):]
				tp.PrintfLine("250 ok")
			case strings.HasPrefix(cmd, "RCPT TO:"):
				g.rcpt = line[len("RCPT TO:"):]
				tp.PrintfLine("250 ok")
			case cmd == "DATA":
				tp.PrintfLine("354 go")
				b, _ := io.ReadAll(tp.DotReader())
				g.data = string(b)
				tp.PrintfLine("250 queued")
			case cmd == "QUIT":
				tp.PrintfLine("221 bye")
				res <- g
				return
			default:
				tp.PrintfLine("250 ok")
			}
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, res
}

func TestSend(t *testing.T) {
	port, res := fakeSMTP(t)
	s := &Sender{Opts: Opts{Host: "127.0.0.1", Port: port, Timeout: 5 * time.Second}, From: "do-not-reply@relaychat.party"}
	long := strings.Repeat("long line ", 20)
	body := "Hi Zoë,\r\n" + long + "\r\nbye\r\n"
	if err := s.Send("zoe@example.com", "Confirm your relaychat.party email address", body); err != nil {
		t.Fatal(err)
	}
	g := <-res
	if g.from != "<do-not-reply@relaychat.party>" || g.rcpt != "<zoe@example.com>" {
		t.Errorf("envelope = %q %q", g.from, g.rcpt)
	}
	hdr, rest, ok := strings.Cut(g.data, "\n\n") // DotReader turns CRLF into LF
	if !ok {
		t.Fatalf("no header/body split: %q", g.data)
	}
	for _, h := range []string{
		"From: <do-not-reply@relaychat.party>",
		"To: <zoe@example.com>",
		"Subject: Confirm your relaychat.party email address",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: quoted-printable",
	} {
		if !strings.Contains(hdr, h) {
			t.Errorf("missing header %q in %q", h, hdr)
		}
	}
	dec, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(rest)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(dec), "\r\n", "\n") != strings.ReplaceAll(body, "\r\n", "\n") {
		t.Errorf("body = %q, want %q", dec, body)
	}
	sc := bufio.NewScanner(strings.NewReader(rest))
	for sc.Scan() {
		if len(sc.Text()) > 76 {
			t.Errorf("encoded line over 76: %q", sc.Text())
		}
	}
}

// Every email has a Date and a Message-ID, which spam filters look for.
func TestSendDateAndMessageID(t *testing.T) {
	port, res := fakeSMTP(t)
	s := &Sender{Opts: Opts{Host: "127.0.0.1", Port: port, Timeout: 5 * time.Second}, From: "do-not-reply@relaychat.party"}
	if err := s.Send("zoe@example.com", "hi", "body"); err != nil {
		t.Fatal(err)
	}
	hdr, _, _ := strings.Cut((<-res).data, "\n\n")
	for _, h := range []string{"\nDate: ", "\nMessage-ID: <"} {
		if !strings.Contains("\n"+hdr, h) {
			t.Errorf("missing %q in %q", strings.TrimSpace(h), hdr)
		}
	}
}

// A newline in a header value is refused, not sent.
func TestSendRefusesHeaderNewline(t *testing.T) {
	s := &Sender{Opts: Opts{Host: "127.0.0.1", Port: 1, Timeout: time.Second}, From: "a@b.c"}
	for _, c := range [][2]string{{"x@y.z\r\nBcc: v@w.x", "s"}, {"x@y.z", "s\nBcc: v@w.x"}} {
		if err := s.Send(c[0], c[1], "b"); err == nil || !strings.Contains(err.Error(), "newline") {
			t.Errorf("Send(%q, %q) = %v, want a newline error", c[0], c[1], err)
		}
	}
}

func TestSendTimeout(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err == nil {
			defer c.Close()
			time.Sleep(5 * time.Second)
		}
	}()
	port, _ := strconv.Atoi(strings.Split(l.Addr().String(), ":")[1])
	for _, ssl := range []bool{false, true} {
		s := &Sender{Opts: Opts{Host: "127.0.0.1", Port: port, SSL: ssl, Timeout: 300 * time.Millisecond}, From: "a@b.c"}
		start := time.Now()
		if err := s.Send("x@y.z", "s", "b"); err == nil {
			t.Fatalf("ssl=%v: want an error", ssl)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("ssl=%v: took %v", ssl, d)
		}
	}
}

// The timeout is per read and write: a server slower than the timeout in
// total, but not on any one reply, still gets the mail.
func TestSendSlowServer(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		tp := textproto.NewConn(c)
		reply := func(s string) { time.Sleep(60 * time.Millisecond); tp.PrintfLine("%s", s) }
		reply("220 slow")
		for {
			line, err := tp.ReadLine()
			if err != nil {
				return
			}
			switch cmd := strings.ToUpper(line); {
			case cmd == "DATA":
				reply("354 go")
				io.ReadAll(tp.DotReader())
				reply("250 queued")
			case cmd == "QUIT":
				reply("221 bye")
				return
			default:
				reply("250 ok")
			}
		}
	}()
	port := l.Addr().(*net.TCPAddr).Port
	s := &Sender{Opts: Opts{Host: "127.0.0.1", Port: port, Timeout: 200 * time.Millisecond}, From: "a@b.c"}
	start := time.Now()
	if err := s.Send("x@y.z", "s", "b"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Fatalf("took %s; the server should be slower than the timeout in total", d)
	}
}
