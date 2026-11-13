package stream

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/login"
	"github.com/lierc/lierc-api/internal/testenv"
)

func (e *env) loginID(token string) int64 {
	e.t.Helper()
	l, ok, err := login.Lookup(context.Background(), e.db, token)
	if err != nil || !ok {
		e.t.Fatalf("lookup: %v %v", ok, err)
	}
	return l.ID
}

// wantConnected waits up to 2s for ConnectedLogins(user) to be want.
func (e *env) wantConnected(user string, want map[int64]bool) {
	e.t.Helper()
	var got map[int64]bool
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if got = e.s.ConnectedLogins(user); reflect.DeepEqual(got, want) {
			return
		}
	}
	e.t.Errorf("ConnectedLogins(%s) = %v, want %v", user, got, want)
}

// A login is connected while it has a stream open: two tabs on one login
// are one connected login, and another user's stream never counts.
func TestConnectedLogins(t *testing.T) {
	e := setup(t)
	alice, bob := e.addUser("alice"), e.addUser("bob")
	a := testenv.Login(t, e.db, alice)
	testenv.Login(t, e.db, alice) // a second login, with no stream: never connected
	c := testenv.Login(t, e.db, bob)
	a1, a2 := e.openToken(a), e.openToken(a)
	e.openToken(c)
	A, C := e.loginID(a), e.loginID(c)
	e.wantConnected(alice, map[int64]bool{A: true})
	e.wantConnected(bob, map[int64]bool{C: true})

	a1.close()
	e.wantConnected(alice, map[int64]bool{A: true})
	a2.close()
	e.wantConnected(alice, map[int64]bool{})
	e.wantConnected("nobody", map[int64]bool{})
}

// A stream the server ends, because its login was ended, drops off.
func TestConnectedLoginsDropsEnded(t *testing.T) {
	e := setup(t)
	e.s.LoginCheck = 50 * time.Millisecond
	alice := e.addUser("alice")
	a := testenv.Login(t, e.db, alice)
	c := e.openToken(a)
	e.wantConnected(alice, map[int64]bool{e.loginID(a): true})
	e.exec(`DELETE FROM login WHERE "user" = $1`, alice)
	if !c.ended(2 * time.Second) {
		t.Fatal("stream of the ended login stayed open")
	}
	e.wantConnected(alice, map[int64]bool{})
}
