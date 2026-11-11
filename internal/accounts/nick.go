package accounts

import "strings"

// IrcNick is Util->irc_nick: anything outside [A-Za-z0-9_] becomes "_", a
// leading digit gets a "_" prefix, and it keeps at most 32 characters.
// It works on characters, as Perl does on a decoded string.
func IrcNick(username string) string {
	nick := []rune(strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		}
		return '_'
	}, username))
	if len(nick) > 0 && nick[0] >= '0' && nick[0] <= '9' {
		nick = append([]rune{'_'}, nick...)
	}
	if len(nick) > 32 {
		nick = nick[:32]
	}
	return string(nick)
}
