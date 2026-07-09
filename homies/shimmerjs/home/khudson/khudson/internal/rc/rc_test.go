package rc

import (
	"bytes"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// serveOnce runs a fake kitty on a unix socket: accepts one connection,
// decodes one DCS frame, responds with resp (already a response struct).
func serveOnce(t *testing.T, resp response) (socket string, got chan envelope) {
	t.Helper()
	socket = filepath.Join(t.TempDir(), "kitty.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan envelope, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		raw, err := readDCS(conn)
		if err != nil {
			return
		}
		var env envelope
		if json.Unmarshal(raw, &env) == nil {
			got <- env
		}
		body, _ := json.Marshal(resp)
		conn.Write([]byte(dcsPrefix))
		conn.Write(body)
		conn.Write([]byte(dcsSuffix))
	}()
	return socket, got
}

func TestGetTextRoundTrip(t *testing.T) {
	data, _ := json.Marshal("hello glass")
	socket, got := serveOnce(t, response{OK: true, Data: data})

	c := New(socket)
	c.Timeout = 2 * time.Second
	text, err := c.GetText(GetTextOpts{Match: "id:7", ANSI: true})
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello glass" {
		t.Fatalf("got %q", text)
	}

	env := <-got
	if env.Cmd != "get-text" {
		t.Fatalf("cmd %q", env.Cmd)
	}
	if env.Version != Version || env.Version != [3]int{0, 47, 4} {
		t.Fatalf("version %v, want the 0.47.4 pin", env.Version)
	}
	payload, _ := json.Marshal(env.Payload)
	if !bytes.Contains(payload, []byte(`"id:7"`)) || !bytes.Contains(payload, []byte(`"ansi":true`)) {
		t.Fatalf("payload %s", payload)
	}
}

func TestRCErrorSurfaces(t *testing.T) {
	socket, _ := serveOnce(t, response{OK: false, Error: "no matching window"})
	c := New(socket)
	c.Timeout = 2 * time.Second
	_, err := c.GetText(GetTextOpts{Match: "id:404"})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("no matching window")) {
		t.Fatalf("err %v, want kitty error surfaced", err)
	}
}

func TestReadDCSSkipsGarbage(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("noise before")
	buf.WriteString(dcsPrefix)
	buf.WriteString(`{"ok":true}`)
	buf.WriteString(dcsSuffix)
	raw, err := readDCS(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"ok":true}` {
		t.Fatalf("got %q", raw)
	}
}

func TestSGRMouse(t *testing.T) {
	press := SGRMouse(0, 12, 3, false)
	if string(press) != "\x1b[<0;12;3M" {
		t.Fatalf("press %q", press)
	}
	release := SGRMouse(0, 12, 3, true)
	if string(release) != "\x1b[<0;12;3m" {
		t.Fatalf("release %q", release)
	}
}

func TestSendBytesBase64(t *testing.T) {
	socket, got := serveOnce(t, response{OK: true})
	c := New(socket)
	c.Timeout = 2 * time.Second
	if err := c.SendBytes("id:1", []byte("\x1b[<0;1;1M")); err != nil {
		t.Fatal(err)
	}
	env := <-got
	payload, _ := json.Marshal(env.Payload)
	if !bytes.Contains(payload, []byte(`"data":"base64:`)) {
		t.Fatalf("payload %s, want base64 data", payload)
	}
}
