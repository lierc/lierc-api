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
		"From: do-not-reply@relaychat.party",
		"To: zoe@example.com",
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
	s := &Sender{Opts: Opts{Host: "127.0.0.1", Port: port, Timeout: 300 * time.Millisecond}, From: "a@b.c"}
	start := time.Now()
	if err := s.Send("x@y.z", "s", "b"); err == nil {
		t.Fatal("want an error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
}
