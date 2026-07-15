package rc

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
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

// TestReadDCSSplitAcrossReads drives 1-byte reads so the prefix and suffix
// land split at every boundary -- the linear-scan watermark must keep
// partial-token tails re-scannable.
func TestReadDCSSplitAcrossReads(t *testing.T) {
	raw, err := readDCS(iotest.OneByteReader(strings.NewReader(
		"noise" + dcsPrefix + `{"ok":true}` + dcsSuffix + "trailer")))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"ok":true}` {
		t.Fatalf("got %q", raw)
	}
}

// junkReader streams unterminated payload forever (with a backstop well
// past the watermark so a broken cap fails the test instead of hanging it).
type junkReader struct{ n int }

func (j *junkReader) Read(p []byte) (int, error) {
	if j.n > 2*readDCSMax {
		return 0, io.EOF
	}
	for i := range p {
		p[i] = 'x'
	}
	j.n += len(p)
	return len(p), nil
}

// TestReadDCSWatermark pins the buffer cap: a peer that never terminates
// the frame errors out at readDCSMax instead of growing without bound.
func TestReadDCSWatermark(t *testing.T) {
	junk := &junkReader{}
	_, err := readDCS(io.MultiReader(strings.NewReader(dcsPrefix), junk))
	if err == nil {
		t.Fatal("unterminated stream did not error")
	}
	if !strings.Contains(err.Error(), "frame terminator") {
		t.Fatalf("err %v, want the watermark error", err)
	}
	if junk.n > 2*readDCSMax {
		t.Fatalf("read %d bytes -- the backstop fired, not the watermark", junk.n)
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
