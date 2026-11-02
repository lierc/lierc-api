package lctl

import (
	"errors"
	"strings"
	"testing"
)

const liveStatus = `{"Id":"c1","Nick":"ann_","Registered":true,
 "Config":{"Host":"irc.libera.chat","Port":6697,"Ssl":true,"Nick":"ann_","User":"ann","Pass":"hunter2","SASL":true,"Alias":"Libera","Channels":["#a"],"Highlight":["ann"]},
 "Status":{"Connected":true,"Message":"Connected","Id":"c1","Host":"irc.libera.chat"},
 "Isupport":["CHANTYPES=#","NETWORK=Libera.Chat","NICKLEN=16"],
 "Caps":{"sasl":"PLAIN","server-time":""},"CapsAcked":["server-time","sasl"],
 "Channels":[{"Name":"#b","Nicks":["@x","ann_","y"],"Topic":{"Topic":"welcome to b","User":"x","Time":1793367009},"Mode":"+nt"},{"Name":"#a","Nicks":["ann_"],"Topic":null,"Mode":"+"}]}`

func TestConnectionInfo(t *testing.T) {
	h := newHarness(t)
	h.hosts["irc.libera.chat"] = []string{"8.8.8.8"}
	u := h.user("ann")
	h.conn("c1", u, `{"Host":"irc.libera.chat","Port":6697,"Ssl":true,"Nick":"ann","User":"ann","Pass":"hunter2","SASL":true,"Alias":"Libera","Channels":["#a","#b"],"Highlight":["ann","annie"]}`)
	if _, err := h.env.Liercd.Do(h.t.Context(), "POST", "c1/create", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	h.liercd.SetStatus("c1", liveStatus)
	if err := h.run("connection-info", "c1"); err != nil {
		t.Fatal(err)
	}
	h.has(
		"c1\n",
		"  user:        ann ("+u+")\n",
		"saved config:\n",
		"  server:      irc.libera.chat:6697 (tls)\n",
		"  nick:        ann\n",
		"  username:    ann\n",
		"  password:    set, sent with SASL\n",
		"  alias:       Libera\n",
		"  channels:    #a, #b\n",
		"  highlight:   ann, annie\n",
		"  valid:       yes\n",
		"liercd:\n",
		"  state:       running, connected\n",
		"  status:      Connected\n",
		"  nick:        ann_\n",
		"  registered:  yes\n",
		"  network:     Libera.Chat\n",
		"  caps:        sasl, server-time\n",
		"  differs:     Nick (running ann_, saved ann)\n",
		"  joined:      2 channels\n",
	)
	h.lacks("hunter2", "#b ", "topic", "nicks")
}

func TestConnectionInfoNotRunning(t *testing.T) {
	h := newHarness(t)
	u := h.user("bob")
	h.conn("c2", u, `{"Host":"127.0.0.1","Port":5005,"Nick":"bob","Pass":"pw"}`)
	if err := h.run("connection-info", "c2"); err != nil {
		t.Fatal(err)
	}
	h.has("  server:      127.0.0.1:5005\n", "  username:    (uses nick)\n", "  password:    set, sent with PASS\n",
		"  channels:    none\n", "  valid:       no: Host 127.0.0.1 points to a private address\n",
		"liercd:\n  state:       not running\n")
	h.lacks("  status:", "  alias:")
}

func TestConnectionInfoRunningWithoutRow(t *testing.T) {
	h := newHarness(t)
	if _, err := h.env.Liercd.Do(h.t.Context(), "POST", "c9/create", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	h.liercd.SetStatus("c9", `{"Nick":"z","Status":{"Connected":false,"Message":"Reconnecting in 15s"},"Config":{"Host":"a.com","Port":6667,"Nick":"z"}}`)
	if err := h.run("connection-info", "c9"); err != nil {
		t.Fatal(err)
	}
	h.has("c9\n  saved:       no row in the database\n", "  state:       running, not connected\n", "  status:      Reconnecting in 15s\n")
	h.lacks("  differs:")
}

func TestConnectionInfoUnknown(t *testing.T) {
	h := newHarness(t)
	h.conn("c1", h.user("cy"), cfg)
	if err := h.run("connection-info", "c1", "nope"); err == nil {
		t.Fatal("no error for an unknown id")
	}
	h.has("nope: no such connection\n", "c1\n")
}

func TestConnectionInfoLiercdDown(t *testing.T) {
	h := newHarness(t)
	h.conn("c1", h.user("cy"), cfg)
	h.env.Liercd.BaseURL = "http://127.0.0.1:1"
	if err := h.run("connection-info", "c1"); err != nil {
		t.Fatal(err)
	}
	h.has("liercd:\n  state:       liercd unreachable: ")
}

func TestConnectionInfoInvalidConfig(t *testing.T) {
	h := newHarness(t)
	h.conn("c1", h.user("cy"), `"not an object"`)
	if err := h.run("connection-info", "c1"); err != nil {
		t.Fatal(err)
	}
	h.has("  valid:       no: config isn't a JSON object\n")
	if strings.Contains(h.out(), "server:") {
		t.Error("printed fields of a non-object config")
	}
}

func TestConnectionInfoUsage(t *testing.T) {
	h := newHarness(t)
	var ue *UsageError
	if err := h.run("connection-info"); !errors.As(err, &ue) {
		t.Fatalf("got %v", err)
	}
}
