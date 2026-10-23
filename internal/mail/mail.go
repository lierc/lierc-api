// Package mail sends the service's email.
package mail

// Mailer sends a plain-text email.
type Mailer interface {
	Send(to, subject, body string) error
}
