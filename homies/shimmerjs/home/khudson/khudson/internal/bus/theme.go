// Theme service: the bus owns the palette. kitty IS the theme (style
// deferral): the HUD kitty's effective colors are fetched over RC
// (get-colors) when a dock adopts, cached here, and broadcast as TypeTheme
// {theme, palette}; `ctl theme day|night` re-colors the HUD kitty via
// set-colors, pairs an m1ddc luminance move, then re-fetches and
// re-broadcasts so docks always style from what the glass actually shows.
package bus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/rc"
)

// ThemeColors is the instance-global color surface of the HUD kitty; fakes
// stand in for tests. Palette maps are kitty color names (kitty.conf
// syntax: foreground, background, color0..) to "#rrggbb".
type ThemeColors interface {
	// GetColors fetches the effective palette of the active window.
	GetColors() (map[string]string, error)
	// SetColors applies colors to every window, configured included.
	SetColors(colors map[string]string) error
	// ResetColors restores the colors the kitty instance started with --
	// the day theme, since the HUD kitty config carries the day include.
	ResetColors() error
}

// Luminance pairs the display backlight with the theme via DDC.
type Luminance interface {
	Set(ctx context.Context, bin, display string, value int) error
}

// hudColors is the bus's ONLY line to the HUD kitty (kitty-panel.sock),
// and it is deliberately not an rc.Client: it exposes get-colors/set-colors
// and nothing else. Those two commands are instance-global -- no window id
// crosses the socket -- which is exactly why they are safe here.
//
// Injection must NEVER ride this client. kitty window ids are per-instance,
// so a substrate window id sent through the HUD socket would either match
// nothing or, on an id collision, land raw SGR bytes in the dock's own PTY,
// which bubbletea parses as real clicks (see the substrateRC comment in
// Run: the scrape substrate is the bus's only injection kitty). Keeping
// this type free of send-text/send-key/launch verbs makes that mistake a
// compile error instead of a comment.
//
// The HUD socket has NO rc password; sending one hangs the dial, so the
// envelope never carries a password field.
type hudColors struct {
	socket  string
	timeout time.Duration
}

func newHudColors(socket string) *hudColors {
	return &hudColors{socket: socket, timeout: 5 * time.Second}
}

func (h *hudColors) GetColors() (map[string]string, error) {
	data, err := h.call("get-colors", struct{}{})
	if err != nil {
		return nil, err
	}
	var out string
	if err := json.Unmarshal(data, &out); err != nil {
		out = string(data)
	}
	pal := parseColors(out)
	if len(pal) == 0 {
		return nil, fmt.Errorf("get-colors: no colors in response %q", truncateStr(out, 120))
	}
	return pal, nil
}

func (h *hudColors) SetColors(colors map[string]string) error {
	enc := make(map[string]any, len(colors))
	for k, v := range colors {
		n, err := hexColor(v)
		if err != nil {
			return fmt.Errorf("set-colors: %s: %w", k, err)
		}
		enc[k] = n
	}
	// kitty wants 24-bit ints; all+configured so every window and future
	// window in the HUD instance follows the theme
	_, err := h.call("set-colors", map[string]any{
		"colors": enc, "all": true, "configured": true,
	})
	return err
}

func (h *hudColors) ResetColors() error {
	// server-side reset to the startup values (kitty 0.47.4 rc/set_colors.py:
	// reset implies all+configured and ignores the colors arg)
	_, err := h.call("set-colors", map[string]any{
		"reset": true, "all": true, "configured": true,
	})
	return err
}

// call mirrors internal/rc's wire framing (<ESC>P@kitty-cmd{json}<ESC>\,
// envelope {cmd, version, payload}) for the two color commands. Kept local
// instead of extending rc.Client so this client's verb surface stays
// colors-only; the protocol version is shared with rc.
func (h *hudColors) call(cmd string, payload any) (json.RawMessage, error) {
	const dcsPrefix = "\x1bP@kitty-cmd"
	const dcsSuffix = "\x1b\\"

	conn, err := net.DialTimeout("unix", h.socket, h.timeout)
	if err != nil {
		return nil, fmt.Errorf("%s: dial %s: %w", cmd, h.socket, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(h.timeout)); err != nil {
		return nil, fmt.Errorf("%s: deadline: %w", cmd, err)
	}

	body, err := json.Marshal(struct {
		Cmd     string `json:"cmd"`
		Version [3]int `json:"version"`
		Payload any    `json:"payload,omitempty"`
	}{Cmd: cmd, Version: rc.Version, Payload: payload})
	if err != nil {
		return nil, fmt.Errorf("%s: marshal: %w", cmd, err)
	}
	if _, err := conn.Write(append(append([]byte(dcsPrefix), body...), dcsSuffix...)); err != nil {
		return nil, fmt.Errorf("%s: write: %w", cmd, err)
	}

	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	for {
		n, rerr := conn.Read(tmp)
		buf.Write(tmp[:n])
		b := buf.Bytes()
		if start := bytes.Index(b, []byte(dcsPrefix)); start >= 0 {
			rest := b[start+len(dcsPrefix):]
			if end := bytes.Index(rest, []byte(dcsSuffix)); end >= 0 {
				var resp struct {
					OK    bool            `json:"ok"`
					Data  json.RawMessage `json:"data,omitempty"`
					Error string          `json:"error,omitempty"`
				}
				if err := json.Unmarshal(rest[:end], &resp); err != nil {
					return nil, fmt.Errorf("%s: bad response: %w", cmd, err)
				}
				if !resp.OK {
					if resp.Error == "" {
						resp.Error = "unspecified rc error"
					}
					return nil, fmt.Errorf("%s: kitty: %s", cmd, resp.Error)
				}
				return resp.Data, nil
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil, fmt.Errorf("%s: connection closed before response frame (got %d bytes)", cmd, buf.Len())
			}
			return nil, fmt.Errorf("%s: %w", cmd, rerr)
		}
	}
}

// parseColors parses `kitten @ get-colors` output: one "key<spaces>#rrggbb"
// line per color. Non-color lines are skipped.
func parseColors(out string) map[string]string {
	pal := make(map[string]string)
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "#") {
			continue
		}
		pal[fields[0]] = fields[1]
	}
	return pal
}

// hexColor parses "#rrggbb" into the 24-bit int kitty's set-colors payload
// wants.
func hexColor(s string) (int, error) {
	hexs, ok := strings.CutPrefix(s, "#")
	if !ok || len(hexs) != 6 {
		return 0, fmt.Errorf("color %q is not #rrggbb", s)
	}
	n, err := strconv.ParseUint(hexs, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("color %q is not #rrggbb", s)
	}
	return int(n), nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// m1ddcLuminance sets external-display luminance by exec'ing m1ddc, display
// resolved by name from `display list` (the brightness module's pattern).
type m1ddcLuminance struct{}

func (m1ddcLuminance) Set(ctx context.Context, bin, display string, value int) error {
	out, err := exec.CommandContext(ctx, bin, "display", "list").Output()
	if err != nil {
		return fmt.Errorf("%s display list: %w", bin, err)
	}
	idx := 0
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[") {
			continue
		}
		bracket, rest, ok := strings.Cut(line[1:], "]")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(bracket))
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(rest), strings.ToLower(display)) {
			idx = n
			break
		}
	}
	if idx == 0 {
		return fmt.Errorf("%s: display %q not found", bin, display)
	}
	if _, err := exec.CommandContext(ctx, bin, "display", strconv.Itoa(idx), "set", "luminance", strconv.Itoa(value)).Output(); err != nil {
		return fmt.Errorf("%s set luminance %d: %w", bin, value, err)
	}
	return nil
}

// ensurePalette fetches the HUD palette once, asynchronously, and broadcasts
// it. Called from the dock greeting: the dock runs inside the HUD kitty, so
// a dock hello IS the kitty liveness gate. A failed fetch stays uncached and
// retries on the next dock hello (or a theme switch).
func (b *Bus) ensurePalette() {
	b.mu.Lock()
	if b.colors == nil || b.palette != nil || b.fetchingPalette {
		b.mu.Unlock()
		return
	}
	b.fetchingPalette = true
	b.mu.Unlock()

	go func() {
		pal, err := b.colors.GetColors()
		b.mu.Lock()
		defer b.mu.Unlock()
		b.fetchingPalette = false
		if err != nil {
			log.Printf("khudson bus: palette fetch: %v", err)
			return
		}
		if b.palette != nil {
			return // a theme switch already installed a fresher palette
		}
		b.palette = pal
		theme := b.themeLocked()
		b.broadcastLocked(proto.Msg{Type: proto.TypeTheme, Theme: theme, Palette: pal})
	}()
}

// themeLocked is the wire theme name; caller holds b.mu ("" = day).
func (b *Bus) themeLocked() string {
	if b.theme == "" {
		return "day"
	}
	return b.theme
}

// switchTheme is the ctl theme verb. Night applies the config night colors
// to the HUD kitty; day resets to its startup theme (the day include).
// Either way the palette is re-fetched and re-broadcast so the cache never
// drifts from the glass. Luminance pairing is best-effort: by the time it
// runs the colors have switched, and a disconnected display must not fail
// the verb. themeMu serializes whole switches so two ctl calls cannot
// interleave their RC ops.
func (b *Bus) switchTheme(theme string) error {
	b.themeMu.Lock()
	defer b.themeMu.Unlock()

	b.mu.Lock()
	themeCfg := b.cfg.Theme
	b.mu.Unlock()

	if b.colors != nil {
		if theme == "night" {
			var colors map[string]string
			if themeCfg != nil {
				colors = themeCfg.Night.Colors
			}
			if len(colors) == 0 {
				// no night palette configured: broadcast-only switch,
				// loud so a missing config block is not silent
				log.Printf("khudson bus: theme night: no theme.night.colors configured; kitty colors unchanged")
			} else if err := b.colors.SetColors(colors); err != nil {
				return fmt.Errorf("theme night: %w", err)
			}
		} else {
			if err := b.colors.ResetColors(); err != nil {
				return fmt.Errorf("theme day: %w", err)
			}
		}
	}

	if b.lum != nil && themeCfg != nil {
		v := themeCfg.Luminance.Day
		if theme == "night" {
			v = themeCfg.Luminance.Night
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := b.lum.Set(ctx, themeCfg.Luminance.Bin, themeCfg.Luminance.Display, v); err != nil {
			log.Printf("khudson bus: theme %s: luminance: %v", theme, err)
		}
		cancel()
	}

	var pal map[string]string
	if b.colors != nil {
		var err error
		if pal, err = b.colors.GetColors(); err != nil {
			log.Printf("khudson bus: theme %s: palette re-fetch: %v", theme, err)
		}
	}

	// set + broadcast under one acquisition so docks never see themes out of
	// order with the state
	b.mu.Lock()
	b.theme = theme
	if pal != nil {
		b.palette = pal
	}
	b.broadcastLocked(proto.Msg{Type: proto.TypeTheme, Theme: theme, Palette: b.palette})
	b.mu.Unlock()
	return nil
}
