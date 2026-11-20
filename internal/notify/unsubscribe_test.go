package notify

import (
	"bytes"
	"io"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/accounts"
	"github.com/lierc/lierc-api/internal/logger"
)

// the token in u1's links; accounts' test pins its value
var unsubscribeVector = accounts.UnsubscribeToken("test-secret", "u1")

// emailFor is the notification email for one highlight, its headers parsed
// and its body decoded.
func emailFor(t *testing.T) (mail.Header, string) {
	t.Helper()
	n := &Notifier{Config: &NotifierConfig{APIURL: "https://relaychat.party", UnsubscribeSecret: "test-secret"}}
	m, err := n.emailMessage([]*logger.LoggedMessage{highlight("c1", time.Now())}, "u1@example.com", "u1")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if _, err := m.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(&b)
	if err != nil {
		t.Fatalf("unreadable email: %v\n%s", err, b.String())
	}
	body, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if err != nil {
		t.Fatal(err)
	}
	return msg.Header, string(body)
}

func TestEmailHeaders(t *testing.T) {
	h, _ := emailFor(t)
	for k, want := range map[string]string{
		"From":                      `"Relaychat Party" <notifications@relaychat.party>`,
		"To":                        "<u1@example.com>",
		"Subject":                   "[#c] < bob> hi leedo",
		"Content-Type":              "text/plain; charset=UTF-8",
		"Content-Transfer-Encoding": "quoted-printable",
		"X-Mailer":                  "relaychat",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	for _, k := range []string{"Date", "Message-Id"} {
		if h.Get(k) == "" {
			t.Errorf("no %s header", k)
		}
	}
}

func TestEmailUnsubscribeLink(t *testing.T) {
	_, body := emailFor(t)
	if !strings.Contains(body, "https://relaychat.party/unsubscribe/#"+unsubscribeVector) {
		t.Errorf("body lacks the signed link:\n%s", body)
	}
	if strings.Contains(body, "https://relaychat.party/unsubscribe/\n") {
		t.Errorf("body still has the bare link:\n%s", body)
	}
}

func TestEmailUnsubscribeHeaders(t *testing.T) {
	header, body := emailFor(t)
	for k, want := range map[string]string{
		"List-Unsubscribe":      "<https://relaychat.party/api/unsubscribe?token=" + unsubscribeVector + ">",
		"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
	} {
		if got := header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if strings.Contains(body, "List-Unsubscribe") {
		t.Errorf("List-Unsubscribe in the body:\n%s", body)
	}
}

func TestConfigUnsubscribeSecret(t *testing.T) {
	c, err := ConfigFromEnv(func(k string) string {
		if k == "UNSUBSCRIBE_SECRET" {
			return "s3"
		}
		return ""
	})
	if err != nil || c.UnsubscribeSecret != "s3" {
		t.Errorf("set: %v %q", err, c.UnsubscribeSecret)
	}
	c, err = ConfigFromEnv(func(string) string { return "" })
	if err != nil || c.UnsubscribeSecret != "changeme" {
		t.Errorf("unset: %v %q, want changeme", err, c.UnsubscribeSecret)
	}
}

// SendEmail delivers the notification email over SMTP_OPTS' server, with
// its headers and unsubscribe link.
func TestSendEmailDelivers(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	data := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		tp := textproto.NewConn(c)
		tp.PrintfLine("220 fake")
		for {
			line, err := tp.ReadLine()
			if err != nil {
				return
			}
			switch strings.ToUpper(line) {
			case "DATA":
				tp.PrintfLine("354 go")
				b, _ := io.ReadAll(tp.DotReader())
				data <- string(b)
				tp.PrintfLine("250 queued")
			case "QUIT":
				tp.PrintfLine("221 bye")
				return
			default:
				tp.PrintfLine("250 ok")
			}
		}
	}()

	n := &Notifier{Config: &NotifierConfig{APIURL: "https://relaychat.party", UnsubscribeSecret: "test-secret"}}
	n.Config.SMTP.Host, n.Config.SMTP.Port = "127.0.0.1", l.Addr().(*net.TCPAddr).Port
	n.Config.SMTP.Timeout = time.Second
	if err := n.SendEmail([]*logger.LoggedMessage{highlight("c1", time.Now())}, "u1@example.com", "u1"); err != nil {
		t.Fatal(err)
	}
	var got string
	select {
	case got = <-data:
	case <-time.After(5 * time.Second):
		t.Fatal("no email received")
	}
	m, err := mail.ReadMessage(strings.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"From":             `"Relaychat Party" <notifications@relaychat.party>`,
		"To":               "<u1@example.com>",
		"List-Unsubscribe": "<https://relaychat.party/api/unsubscribe?token=" + unsubscribeVector + ">",
	} {
		if v := m.Header.Get(k); v != want {
			t.Errorf("%s: %q, want %q", k, v, want)
		}
	}
}
