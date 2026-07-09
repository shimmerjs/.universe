package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// fakeColors is a ThemeColors that behaves like a kitty: SetColors merges
// into the effective palette, ResetColors restores the startup palette.
type fakeColors struct {
	mu      sync.Mutex
	startup map[string]string
	palette map[string]string
	getErr  error
	setErr  error
	sets    []map[string]string
	resets  int
	gets    int
}

func newFakeColors(startup map[string]string) *fakeColors {
	return &fakeColors{startup: startup, palette: maps.Clone(startup)}
}

func (f *fakeColors) GetColors() (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.getErr != nil {
		return nil, f.getErr
	}
	return maps.Clone(f.palette), nil
}

func (f *fakeColors) SetColors(colors map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.sets = append(f.sets, maps.Clone(colors))
	maps.Copy(f.palette, colors)
	return nil
}

func (f *fakeColors) ResetColors() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.resets++
	f.palette = maps.Clone(f.startup)
	return nil
}

// fakeLum records luminance moves.
type fakeLum struct {
	mu    sync.Mutex
	calls []lumCall
	err   error
}

type lumCall struct {
	bin, display string
	value        int
}

func (f *fakeLum) Set(_ context.Context, bin, display string, value int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, lumCall{bin: bin, display: display, value: value})
	return nil
}

func (f *fakeLum) last(t *testing.T) lumCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no luminance call recorded")
	}
	return f.calls[len(f.calls)-1]
}

func dayPalette() map[string]string {
	return map[string]string{
		"background": "#232a2e",
		"foreground": "#d3c6aa",
		"color2":     "#a7c080",
	}
}

func nightColors() map[string]string {
	return map[string]string{
		"background": "#000000",
		"foreground": "#e6dcbe",
	}
}

func themeTestBus(t *testing.T) (*Bus, *fakeColors, *fakeLum) {
	t.Helper()
	cfg := &config.Config{
		Widgets: map[string]config.Widget{},
		Layouts: map[string]config.Layout{"main": {Kind: "dock-grid"}},
		Layout:  "main",
		Theme: &config.Theme{
			Night:     config.NightTheme{Colors: nightColors()},
			Luminance: config.ThemeLuminance{Bin: "m1ddc", Display: "XENEON EDGE", Night: 10, Day: 60},
		},
	}
	fc := newFakeColors(dayPalette())
	fl := &fakeLum{}
	b := &Bus{
		cfg:    cfg,
		reg:    NewRegistry(cfg),
		docks:  make(map[net.Conn]*json.Encoder),
		colors: fc,
		lum:    fl,
	}
	return b, fc, fl
}

// addDecodingDock registers a dock connection and decodes everything the bus
// broadcasts to it.
func addDecodingDock(t *testing.T, b *Bus) <-chan proto.Msg {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	ch := make(chan proto.Msg, 16)
	go func() {
		dec := json.NewDecoder(client)
		for {
			var m proto.Msg
			if err := dec.Decode(&m); err != nil {
				close(ch)
				return
			}
			ch <- m
		}
	}()
	b.mu.Lock()
	b.docks[server] = json.NewEncoder(server)
	b.mu.Unlock()
	return ch
}

func wantThemeMsg(t *testing.T, ch <-chan proto.Msg) proto.Msg {
	t.Helper()
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				t.Fatal("dock connection closed before a theme broadcast")
			}
			if m.Type == proto.TypeTheme {
				return m
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no theme broadcast within 2s")
		}
	}
}

func TestParseColors(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want map[string]string
	}{
		{
			name: "aligned get-colors output",
			in:   "active_border_color   #83c092\nbackground            #232a2e\ncolor0                #414b50\ncolor10               #a7c080\nforeground            #d3c6aa",
			want: map[string]string{
				"active_border_color": "#83c092",
				"background":          "#232a2e",
				"color0":              "#414b50",
				"color10":             "#a7c080",
				"foreground":          "#d3c6aa",
			},
		},
		{
			name: "junk lines skipped",
			in:   "background #232a2e\n\nnot a color line at all\ncursor none\nforeground #d3c6aa\n",
			want: map[string]string{
				"background": "#232a2e",
				"foreground": "#d3c6aa",
			},
		},
		{
			name: "empty",
			in:   "",
			want: map[string]string{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := parseColors(tt.in)
			if !maps.Equal(got, tt.want) {
				t.Fatalf("parseColors = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHexColor(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want int
		err  bool
	}{
		{in: "#000000", want: 0},
		{in: "#232a2e", want: 0x232a2e},
		{in: "#FFFFFF", want: 0xffffff},
		{in: "232a2e", err: true},
		{in: "#fff", err: true},
		{in: "#gggggg", err: true},
	} {
		got, err := hexColor(tt.in)
		if tt.err != (err != nil) {
			t.Fatalf("hexColor(%q) err = %v, want err=%v", tt.in, err, tt.err)
		}
		if err == nil && got != tt.want {
			t.Fatalf("hexColor(%q) = %#x, want %#x", tt.in, got, tt.want)
		}
	}
}

// TestSwitchThemeNight: ctl theme night applies the config night colors to
// the HUD kitty, pairs the night luminance, re-fetches, and broadcasts the
// effective palette with the theme name.
func TestSwitchThemeNight(t *testing.T) {
	b, fc, fl := themeTestBus(t)
	ch := addDecodingDock(t, b)

	resp := b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "theme", Arg: "night"})
	if !resp.OK {
		t.Fatalf("resp = %+v, want ok", resp)
	}
	fc.mu.Lock()
	sets := len(fc.sets)
	fc.mu.Unlock()
	if sets != 1 {
		t.Fatalf("set-colors calls = %d, want 1", sets)
	}
	if got := fl.last(t); got.value != 10 || got.bin != "m1ddc" || got.display != "XENEON EDGE" {
		t.Fatalf("luminance call = %+v, want m1ddc/XENEON EDGE/10", got)
	}

	m := wantThemeMsg(t, ch)
	if m.Theme != "night" {
		t.Fatalf("broadcast theme = %q, want night", m.Theme)
	}
	// the broadcast palette is the re-fetched effective palette: night
	// overrides merged over the day base
	if m.Palette["background"] != "#000000" || m.Palette["color2"] != "#a7c080" {
		t.Fatalf("broadcast palette = %v, want night overrides over day base", m.Palette)
	}

	b.mu.Lock()
	theme, pal := b.theme, b.palette
	b.mu.Unlock()
	if theme != "night" || pal["background"] != "#000000" {
		t.Fatalf("bus cache theme=%q palette=%v, want night/black floor", theme, pal)
	}
}

// TestSwitchThemeDay: ctl theme day resets the kitty to its startup colors
// (never an inlined palette), restores the day luminance, and broadcasts the
// re-fetched palette.
func TestSwitchThemeDay(t *testing.T) {
	b, fc, fl := themeTestBus(t)
	ch := addDecodingDock(t, b)

	if resp := b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "theme", Arg: "night"}); !resp.OK {
		t.Fatalf("night resp = %+v", resp)
	}
	wantThemeMsg(t, ch)
	if resp := b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "theme", Arg: "day"}); !resp.OK {
		t.Fatalf("day resp = %+v", resp)
	}
	fc.mu.Lock()
	resets := fc.resets
	fc.mu.Unlock()
	if resets != 1 {
		t.Fatalf("resets = %d, want 1", resets)
	}
	if got := fl.last(t); got.value != 60 {
		t.Fatalf("luminance = %d, want day 60", got.value)
	}
	m := wantThemeMsg(t, ch)
	if m.Theme != "day" || !maps.Equal(m.Palette, dayPalette()) {
		t.Fatalf("day broadcast = %q %v, want day + startup palette", m.Theme, m.Palette)
	}
}

// TestSwitchThemeSetColorsFailure: a failed set-colors fails the verb and
// leaves theme state and docks untouched.
func TestSwitchThemeSetColorsFailure(t *testing.T) {
	b, fc, _ := themeTestBus(t)
	ch := addDecodingDock(t, b)
	fc.mu.Lock()
	fc.setErr = fmt.Errorf("kitty gone")
	fc.mu.Unlock()

	resp := b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "theme", Arg: "night"})
	if resp.OK || !strings.Contains(resp.Error, "kitty gone") {
		t.Fatalf("resp = %+v, want kitty error", resp)
	}
	b.mu.Lock()
	theme := b.theme
	b.mu.Unlock()
	if theme != "" {
		t.Fatalf("theme = %q, want unchanged", theme)
	}
	select {
	case m := <-ch:
		t.Fatalf("unexpected broadcast after failed switch: %+v", m)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestSwitchThemeNoNightColors: a config without night colors still flips
// and broadcasts the theme without touching the kitty without touching the kitty.
func TestSwitchThemeNoNightColors(t *testing.T) {
	b, fc, _ := themeTestBus(t)
	b.cfg.Theme = nil
	ch := addDecodingDock(t, b)

	resp := b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "theme", Arg: "night"})
	if !resp.OK {
		t.Fatalf("resp = %+v, want ok", resp)
	}
	fc.mu.Lock()
	sets := len(fc.sets)
	fc.mu.Unlock()
	if sets != 0 {
		t.Fatalf("set-colors called %d times with no night colors", sets)
	}
	if m := wantThemeMsg(t, ch); m.Theme != "night" {
		t.Fatalf("broadcast = %+v, want theme night", m)
	}
}

// TestAdoptFetchesAndBroadcastsPalette: the first dock hello (the kitty
// liveness gate) triggers the get-colors fetch; the palette lands as a
// TypeTheme broadcast and later greetings replay it inline.
func TestAdoptFetchesAndBroadcastsPalette(t *testing.T) {
	b, fc, _ := themeTestBus(t)

	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.serveDock(server, json.NewEncoder(server), json.NewDecoder(server),
			proto.Msg{Type: proto.TypeHello, Role: proto.RoleDock, Cols: 320, Rows: 18})
	}()

	dec := json.NewDecoder(client)
	msgs := make(chan proto.Msg, 16)
	go func() {
		for {
			var m proto.Msg
			if err := dec.Decode(&m); err != nil {
				close(msgs)
				return
			}
			msgs <- m
		}
	}()

	// greeting: layout, then theme with no palette (nothing fetched yet)
	if m := wantThemeMsg(t, msgs); m.Theme != "day" || m.Palette != nil {
		t.Fatalf("greeting theme = %+v, want day with no palette", m)
	}
	// the adopt fetch runs async and broadcasts the palette
	if m := wantThemeMsg(t, msgs); !maps.Equal(m.Palette, dayPalette()) {
		t.Fatalf("adopt broadcast palette = %v, want %v", m.Palette, dayPalette())
	}
	fc.mu.Lock()
	gets := fc.gets
	fc.mu.Unlock()
	if gets != 1 {
		t.Fatalf("get-colors calls = %d, want 1", gets)
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveDock did not exit on close")
	}

	// second dock: palette is cached, greeting carries it inline and no new
	// fetch fires
	client2, server2 := net.Pipe()
	defer client2.Close()
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		b.serveDock(server2, json.NewEncoder(server2), json.NewDecoder(server2),
			proto.Msg{Type: proto.TypeHello, Role: proto.RoleDock, Cols: 320, Rows: 18})
	}()
	dec2 := json.NewDecoder(client2)
	msgs2 := make(chan proto.Msg, 16)
	go func() {
		for {
			var m proto.Msg
			if err := dec2.Decode(&m); err != nil {
				close(msgs2)
				return
			}
			msgs2 <- m
		}
	}()
	if m := wantThemeMsg(t, msgs2); !maps.Equal(m.Palette, dayPalette()) {
		t.Fatalf("cached greeting palette = %v, want %v", m.Palette, dayPalette())
	}
	fc.mu.Lock()
	gets = fc.gets
	fc.mu.Unlock()
	if gets != 1 {
		t.Fatalf("get-colors calls = %d after cached greeting, want still 1", gets)
	}
	client2.Close()
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("serveDock did not exit on close")
	}
}

// fakeKittyRC serves the kitty RC wire protocol on a unix socket: it records
// each request envelope and answers get-colors/set-colors.
func fakeKittyRC(t *testing.T, getColorsOutput string) (socket string, reqs <-chan map[string]any) {
	t.Helper()
	dir, err := os.MkdirTemp("", "khudson-theme")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket = filepath.Join(dir, "kitty-panel.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	const prefix = "\x1bP@kitty-cmd"
	const suffix = "\x1b\\"
	ch := make(chan map[string]any, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 0, 4096)
				tmp := make([]byte, 1024)
				for {
					n, err := c.Read(tmp)
					buf = append(buf, tmp[:n]...)
					if i := strings.Index(string(buf), suffix); i >= 0 {
						frame := strings.TrimPrefix(string(buf[:i]), prefix)
						var env map[string]any
						if err := json.Unmarshal([]byte(frame), &env); err != nil {
							return
						}
						ch <- env
						var resp string
						if env["cmd"] == "get-colors" {
							data, _ := json.Marshal(getColorsOutput)
							resp = fmt.Sprintf(`{"ok":true,"data":%s}`, data)
						} else {
							resp = `{"ok":true}`
						}
						_, _ = c.Write([]byte(prefix + resp + suffix))
						return
					}
					if err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return socket, ch
}

// TestHudColorsWire exercises the real client against a fake kitty socket:
// framing, envelope shape (and the hard rule that no password field ever
// goes on the HUD socket -- a password hangs the real dial), payload
// encoding, and output parsing.
func TestHudColorsWire(t *testing.T) {
	socket, reqs := fakeKittyRC(t, "background   #232a2e\nforeground   #d3c6aa")
	h := newHudColors(socket)
	h.timeout = 2 * time.Second

	pal, err := h.GetColors()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"background": "#232a2e", "foreground": "#d3c6aa"}
	if !maps.Equal(pal, want) {
		t.Fatalf("GetColors = %v, want %v", pal, want)
	}
	env := <-reqs
	if env["cmd"] != "get-colors" {
		t.Fatalf("cmd = %v, want get-colors", env["cmd"])
	}
	if _, hasPW := env["password"]; hasPW {
		t.Fatal("envelope carries a password; the HUD socket has none and a password hangs the dial")
	}
	if env["version"] == nil {
		t.Fatal("envelope missing protocol version")
	}

	if err := h.SetColors(map[string]string{"background": "#000000", "foreground": "#e6dcbe"}); err != nil {
		t.Fatal(err)
	}
	env = <-reqs
	if env["cmd"] != "set-colors" {
		t.Fatalf("cmd = %v, want set-colors", env["cmd"])
	}
	payload, _ := env["payload"].(map[string]any)
	if payload["all"] != true || payload["configured"] != true {
		t.Fatalf("payload = %v, want all+configured", payload)
	}
	colors, _ := payload["colors"].(map[string]any)
	if colors["background"] != float64(0) || colors["foreground"] != float64(0xe6dcbe) {
		t.Fatalf("colors = %v, want 24-bit ints", colors)
	}

	if err := h.ResetColors(); err != nil {
		t.Fatal(err)
	}
	env = <-reqs
	payload, _ = env["payload"].(map[string]any)
	if payload["reset"] != true || payload["all"] != true || payload["configured"] != true {
		t.Fatalf("reset payload = %v, want reset+all+configured", payload)
	}

	// a bad color never reaches the wire
	if err := h.SetColors(map[string]string{"background": "black"}); err == nil {
		t.Fatal("SetColors accepted a non-hex color")
	}
}
