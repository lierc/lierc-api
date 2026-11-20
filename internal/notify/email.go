package notify

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/lierc/lierc-api/internal/accounts"
	"github.com/lierc/lierc-api/internal/logger"
	gomail "github.com/wneessen/go-mail"
)

// SendEmail emails ms to emailAddress, for user.
func (n *Notifier) SendEmail(ms []*logger.LoggedMessage, emailAddress, user string) error {
	m, err := n.emailMessage(ms, emailAddress, user)
	if err != nil {
		return err
	}
	return n.Config.SMTP.Send(m)
}

// emailMessage is the email of ms, with user's unsubscribe link and the
// one-click List-Unsubscribe headers (RFC 8058).
func (n *Notifier) emailMessage(ms []*logger.LoggedMessage, emailAddress, user string) (*gomail.Msg, error) {
	var (
		lines   []string
		subject string
	)

	for _, m := range ms {
		from := m.Message.Prefix.Name
		text := m.Message.Params[1]
		connection := m.ConnectionId

		var channel string

		if m.Message.Direct {
			channel = m.Message.Prefix.Name
		} else {
			channel = m.Message.Params[0]
		}

		line := fmt.Sprintf("    [%s] < %s> %s\n    %s/app/#/%s/%s", channel, from, text, n.Config.APIURL, connection, url.PathEscape(channel))
		lines = append(lines, line)

		if subject == "" {
			subject = fmt.Sprintf("[%s] < %s> %s", channel, from, text)
		}
	}

	token := accounts.UnsubscribeToken(n.Config.UnsubscribeSecret, user)

	m := gomail.NewMsg()
	if err := m.FromFormat("Relaychat Party", "notifications@relaychat.party"); err != nil {
		return nil, err
	}
	if err := m.To(emailAddress); err != nil {
		return nil, err
	}
	m.Subject(subject)
	m.SetUserAgent("relaychat")
	m.SetGenHeader("List-Unsubscribe", "<"+n.Config.APIURL+"/api/unsubscribe?token="+token+">")
	m.SetGenHeader("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
	m.SetBodyString(gomail.TypeTextPlain,
		"You were mentioned in the following channels:\n\n"+
			strings.Join(lines, "\n\n")+"\n\n"+
			"This message is coming from the relaychat.party IRC client. You enabled email notifications in the client. When someone mentions your name, or uses a word that matches a highlight term you configured, you will recieve an email from us. If you would like to stop recieving these messages, please use this link to unsubscribe: "+n.Config.APIURL+"/unsubscribe/#"+token+"\n\n"+
			"Thank you for using relaychat.party!\n")
	return m, nil
}
