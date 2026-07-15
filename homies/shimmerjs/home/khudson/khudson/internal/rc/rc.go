// Package rc speaks kitty's remote-control wire protocol raw over a unix
// socket: <ESC>P@kitty-cmd{json}<ESC>\ framing both directions, envelope
// {cmd, version, no_response, payload}, response {ok, data, error} per
// https://sw.kovidgoyal.net/kitty/rc_protocol/. No third-party client
// exists; this is deliberately small and defensive about response shapes.
package rc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

// Version is the protocol version khudson claims. It must stay <= the running
// kitty (0.47.4 pinned in nixpkgs today; no flake check guards bumps in
// either direction yet).
var Version = [3]int{0, 47, 4}

const (
	dcsPrefix = "\x1bP@kitty-cmd"
	dcsSuffix = "\x1b\\"
)

// Client issues RC commands to one kitty instance. One connection per
// command, like kitten @ does; safe for concurrent use.
type Client struct {
	Socket  string
	Timeout time.Duration // per call; zero means 5s
}

// New returns a client for the kitty listening on socket.
func New(socket string) *Client {
	return &Client{Socket: socket, Timeout: 5 * time.Second}
}

type envelope struct {
	Cmd     string `json:"cmd"`
	Version [3]int `json:"version"`
	Payload any    `json:"payload,omitempty"`
}

type response struct {
	OK        bool            `json:"ok"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     string          `json:"error,omitempty"`
	Traceback string          `json:"tb,omitempty"`
}

func (c *Client) call(cmd string, payload any) (json.RawMessage, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	conn, err := net.DialTimeout("unix", c.Socket, timeout)
	if err != nil {
		return nil, fmt.Errorf("%s: dial %s: %w", cmd, c.Socket, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("%s: deadline: %w", cmd, err)
	}

	body, err := json.Marshal(envelope{Cmd: cmd, Version: Version, Payload: payload})
	if err != nil {
		return nil, fmt.Errorf("%s: marshal: %w", cmd, err)
	}
	msg := make([]byte, 0, len(dcsPrefix)+len(body)+len(dcsSuffix))
	msg = append(msg, dcsPrefix...)
	msg = append(msg, body...)
	msg = append(msg, dcsSuffix...)
	if _, err := conn.Write(msg); err != nil {
		return nil, fmt.Errorf("%s: write: %w", cmd, err)
	}

	raw, err := readDCS(conn)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cmd, err)
	}
	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%s: bad response %q: %w", cmd, truncate(raw, 200), err)
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "unspecified rc error"
		}
		return nil, fmt.Errorf("%s: kitty: %s", cmd, resp.Error)
	}
	return resp.Data, nil
}

// readDCSMax bounds the response buffer: the largest legitimate frames
// (get-text --ansi of the full glass, ls of a busy tree) run tens of KB, so
// a peer still streaming at this watermark is broken or hostile and errors
// out instead of growing the buffer without bound.
const readDCSMax = 8 << 20

// readDCS scans the stream for one <ESC>P@kitty-cmd ... <ESC>\ frame and
// returns the bytes between. Anything before the prefix is skipped; EOF
// without a complete frame is an error. The scan is linear: scanned marks
// how far the buffer is known token-free, backed off by one partial-token
// tail so a prefix or suffix split across reads still matches.
func readDCS(r io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	start := -1 // index just past the prefix, once seen
	scanned := 0
	for {
		n, rerr := r.Read(tmp)
		buf.Write(tmp[:n])
		b := buf.Bytes()
		if start < 0 {
			if i := bytes.Index(b[scanned:], []byte(dcsPrefix)); i >= 0 {
				start = scanned + i + len(dcsPrefix)
				scanned = start
			} else {
				scanned = max(0, len(b)-len(dcsPrefix)+1)
			}
		}
		if start >= 0 {
			if i := bytes.Index(b[scanned:], []byte(dcsSuffix)); i >= 0 {
				return b[start : scanned+i], nil
			}
			scanned = max(start, len(b)-len(dcsSuffix)+1)
		}
		if buf.Len() > readDCSMax {
			return nil, fmt.Errorf("response exceeds %d bytes without a frame terminator", readDCSMax)
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil, fmt.Errorf("connection closed before response frame (got %d bytes)", buf.Len())
			}
			return nil, rerr
		}
	}
}

// dataString unwraps a data field: kitty returns command output as a JSON
// string. Defensively passes through non-string data as raw text.
func dataString(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return s
	}
	return string(data)
}

func truncate(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}
