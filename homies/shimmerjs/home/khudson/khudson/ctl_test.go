package main

import (
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// TestCtlGrammar pins the verb grammar: bad verbs error before any dial --
// the socket flag points at a path nothing listens on, so a dial attempt
// would surface as "bus not reachable" instead of the grammar error.
func TestCtlGrammar(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "no-bus.sock")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no args", nil, "need a command"},
		{"unknown verb", []string{"bogus"}, `unknown command "bogus"`},
		{"layout without name", []string{"layout"}, "need a layout name"},
		{"theme without name", []string{"theme"}, "need day or night"},
		{"caffeinate bad arg", []string{"caffeinate", "sideways"}, "need on, off, or toggle"},
	}
	for _, c := range cases {
		err := cmdCtl(append([]string{"-socket", sock}, c.args...))
		if err == nil {
			t.Errorf("%s: cmdCtl(%v) = nil, want error", c.name, c.args)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// startFakeBus listens on a unix socket, accepts one conn, decodes the hello
// and ctl messages, answers resp, and delivers the decoded pair.
func startFakeBus(t *testing.T, resp proto.Msg) (string, <-chan [2]proto.Msg) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "bus.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan [2]proto.Msg, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		dec := json.NewDecoder(conn)
		var hello, ctl proto.Msg
		if dec.Decode(&hello) != nil || dec.Decode(&ctl) != nil {
			return
		}
		if json.NewEncoder(conn).Encode(resp) != nil {
			return
		}
		got <- [2]proto.Msg{hello, ctl}
	}()
	return sock, got
}

// TestCtlWire pins the wire contract: hello (TypeHello/RoleCtl) then the ctl
// message carrying the verb's Cmd/Arg, and an OK response returns nil.
func TestCtlWire(t *testing.T) {
	cases := []struct {
		args     []string
		cmd, arg string
	}{
		{[]string{"reload"}, "reload", ""},
		{[]string{"status"}, "status", ""},
		{[]string{"layout", "main"}, "layout", "main"},
		{[]string{"theme", "night"}, "theme", "night"},
		{[]string{"caffeinate", "toggle"}, "caffeinate", "toggle"},
	}
	for _, c := range cases {
		sock, got := startFakeBus(t, proto.Msg{Type: proto.TypeResp, OK: true})
		if err := cmdCtl(append([]string{"-socket", sock}, c.args...)); err != nil {
			t.Errorf("%v: cmdCtl = %v, want nil on ok resp", c.args, err)
			continue
		}
		var msgs [2]proto.Msg
		select {
		case msgs = <-got:
		case <-time.After(5 * time.Second):
			t.Fatalf("%v: fake bus never saw both messages", c.args)
		}
		hello, ctl := msgs[0], msgs[1]
		if hello.Type != proto.TypeHello || hello.Role != proto.RoleCtl {
			t.Errorf("%v: hello = %+v, want type=%s role=%s", c.args, hello, proto.TypeHello, proto.RoleCtl)
		}
		if ctl.Type != proto.TypeCtl || ctl.Cmd != c.cmd || ctl.Arg != c.arg {
			t.Errorf("%v: ctl = %+v, want type=%s cmd=%q arg=%q", c.args, ctl, proto.TypeCtl, c.cmd, c.arg)
		}
	}
}

// TestCtlRespError pins error surfacing: OK=false comes back as the resp
// Error verbatim.
func TestCtlRespError(t *testing.T) {
	sock, got := startFakeBus(t, proto.Msg{Type: proto.TypeResp, Error: "no such layout"})
	err := cmdCtl([]string{"-socket", sock, "layout", "nope"})
	if err == nil || err.Error() != "no such layout" {
		t.Errorf("cmdCtl = %v, want %q", err, "no such layout")
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("fake bus never saw both messages")
	}
}
