//go:build darwin

package bus

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/rc"
)

// TestSpike1Live is the spike 1 harness: it spawns a dedicated kitty
// (--config NONE, socket-only RC), drives the REAL Supervisor/Scraper path
// to materialize minimized os-windows (kitty has no hidden state; spike 1
// finding), and answers the spike questions live:
//
//  1. does get-text --ansi return a complete, truecolor-styled screen?
//  2. does an off-screen os-window's screen model keep updating (PTY
//     parsing render-independent)?
//  3. does send-text (base64) deliver raw ESC bytes -- SGR mouse reports --
//     verbatim to the child PTY, and does btop react?
//
// Opt-in only: it opens a GUI kitty window on the current session.
//
//	KHUDSON_SPIKE1=1 go test ./internal/bus -run TestSpike1Live -v
//
// KHUDSON_SPIKE1_BTOP overrides the btop binary (e.g. a nix store path).
func TestSpike1Live(t *testing.T) {
	if os.Getenv("KHUDSON_SPIKE1") == "" {
		t.Skip("live spike: set KHUDSON_SPIKE1=1 (spawns a GUI kitty)")
	}
	kittyBin, err := exec.LookPath("kitty")
	if err != nil {
		t.Fatalf("kitty not on PATH: %v", err)
	}
	btopBin := os.Getenv("KHUDSON_SPIKE1_BTOP")
	if btopBin == "" {
		btopBin, err = exec.LookPath("btop")
		if err != nil {
			t.Fatalf("btop not on PATH and KHUDSON_SPIKE1_BTOP unset: %v", err)
		}
	}

	sock := filepath.Join(t.TempDir(), "kitty.sock")
	kitty := exec.Command(kittyBin,
		"--config", "NONE",
		"-o", "allow_remote_control=socket-only",
		"--listen-on", "unix:"+sock,
		"--title", "khudson-spike1",
		"--start-as", "minimized",
	)
	kitty.Stdout = os.Stderr
	kitty.Stderr = os.Stderr
	if err := kitty.Start(); err != nil {
		t.Fatalf("start kitty: %v", err)
	}
	t.Cleanup(func() {
		_ = kitty.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _, _ = kitty.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = kitty.Process.Kill()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Logf("cleanup: kitty (pid %d) did not exit", kitty.Process.Pid)
			}
		}
	})

	client := rc.New(sock)
	waitFor(t, 15*time.Second, "kitty RC socket answering ls", func() bool {
		_, err := client.LS()
		return err == nil
	})

	ctx := context.Background()
	sup := NewSupervisor(client)
	scraper := NewScraper(client)

	// isolate btop from the user's real config (it writes on exit)
	cfgDir := t.TempDir()
	btopWidget := config.Widget{
		ID: "spike1-btop",
		Render: config.Render{
			Kind: "exec",
			Argv: []string{"/usr/bin/env", "XDG_CONFIG_HOME=" + cfgDir, btopBin},
		},
	}
	st := &WidgetState{Widget: btopWidget, Cols: 120, Rows: 30}
	if err := sup.Ensure(ctx, st); err != nil {
		t.Fatalf("Ensure(btop): %v", err)
	}
	t.Logf("Ensure: minimized os-window launched, kitty window id=%d", st.WindowID)
	match := fmt.Sprintf("id:%d", st.WindowID)

	// --- ls: user-var binding, geometry (resize settles async), Adopt
	var tree []rc.OSWindow
	var w rc.Window
	waitFor(t, 10*time.Second, "hidden window bound by user var at 120x30 cells", func() bool {
		var lsErr error
		tree, lsErr = client.LS()
		if lsErr != nil {
			return false
		}
		var ok bool
		w, ok = rc.FindWindowByUserVar(tree, UserVarWidget, btopWidget.ID)
		return ok && w.Columns == st.Cols && w.Lines == st.Rows
	})
	if w.ID != st.WindowID {
		t.Errorf("ls: user-var window id=%d, launch returned %d", w.ID, st.WindowID)
	}
	t.Logf("ls: window %d bound by user var, settled at %d cols x %d lines", w.ID, w.Columns, w.Lines)
	// minimized substrate: kitty's ls has no visibility field (verified
	// against boss.OSWindowDict, 0.47.4), so the assertable invariant is
	// only that the substrate never takes focus; invisibility itself is an
	// eyeball check (a minimized window shows as a Dock tile, not on screen)
	if osw, ok := osWindowOf(tree, st.WindowID); !ok {
		t.Errorf("ls: window %d not under any os-window", st.WindowID)
	} else if osw.IsFocused {
		t.Errorf("minimized substrate: os-window %d stole focus", osw.ID)
	}
	reg := NewRegistry(&config.Config{Widgets: map[string]config.Widget{btopWidget.ID: btopWidget}})
	if n := sup.Adopt(tree, reg); n != 1 {
		t.Errorf("Adopt: adopted %d windows, want 1", n)
	} else if ast, _ := reg.Get(btopWidget.ID); ast.WindowID != st.WindowID {
		t.Errorf("Adopt: rebound to window %d, want %d", ast.WindowID, st.WindowID)
	}

	// --- wait for btop to paint a full screen
	waitFor(t, 30*time.Second, "btop first paint", func() bool {
		plain, err := client.GetText(rc.GetTextOpts{Match: match})
		return err == nil && nonBlankLines(plain) >= 20
	})

	// --- fidelity: scrape via the real Scraper (ANSI)
	ansiSnap := scrapeOnce(t, scraper, st)
	plain0, err := client.GetText(rc.GetTextOpts{Match: match})
	if err != nil {
		t.Fatalf("get-text plain: %v", err)
	}
	// kitty emits colon-form truecolor (38:2:r:g:b), often merged with
	// other params in one CSI (e.g. \x1b[22;1;38:2:181:64:64m)
	fgTruecolor := regexp.MustCompile(`38[:;]2[:;]\d+[:;]\d+[:;]\d+`).FindAllString(ansiSnap, -1)
	bgTruecolor := regexp.MustCompile(`48[:;]2[:;]\d+[:;]\d+[:;]\d+`).FindAllString(ansiSnap, -1)
	bold := countSGRParam(ansiSnap, "1")
	t.Logf("fidelity: %d bytes ANSI (%d bytes plain), %d truecolor fg, %d truecolor bg, %d bold",
		len(ansiSnap), len(plain0), len(fgTruecolor), len(bgTruecolor), bold)
	if len(fgTruecolor) == 0 {
		t.Errorf("fidelity: no truecolor fg sequences in ANSI scrape")
	}
	if gotLines := len(splitScreen(ansiSnap)); gotLines != st.Rows {
		t.Errorf("fidelity: ANSI scrape has %d lines, window has %d", gotLines, st.Rows)
	}
	if gotLines := len(splitScreen(plain0)); gotLines != st.Rows {
		t.Errorf("fidelity: plain scrape has %d lines, window has %d", gotLines, st.Rows)
	}
	bankFixture(t, "spike1-btop.ansi", []byte(ansiSnap))

	// --- minimized window keeps updating (render-independent PTY parsing)
	time.Sleep(3 * time.Second)
	plain1, err := client.GetText(rc.GetTextOpts{Match: match})
	if err != nil {
		t.Fatalf("get-text plain (second): %v", err)
	}
	if changed := diffLines(plain0, plain1); changed == 0 {
		t.Errorf("minimized window frozen: identical screen 3s apart")
	} else {
		t.Logf("minimized update: %d/%d lines changed after 3s", changed, st.Rows)
	}

	// --- raw ESC delivery: deterministic echo harness (cat -v in a second
	//     hidden window renders injected bytes as visible text)
	echoWidget := config.Widget{
		ID: "spike1-echo",
		Render: config.Render{
			Kind: "exec",
			Argv: []string{"sh", "-c", "stty -echo -icanon min 1 time 0; exec cat -v"},
		},
	}
	est := &WidgetState{Widget: echoWidget, Cols: 80, Rows: 10}
	if err := sup.Ensure(ctx, est); err != nil {
		t.Fatalf("Ensure(echo): %v", err)
	}
	emch := fmt.Sprintf("id:%d", est.WindowID)
	report := rc.SGRMouse(0, 5, 3, false)
	if err := client.SendBytes(emch, report); err != nil {
		t.Fatalf("send-text (SGR bytes): %v", err)
	}
	if err := client.SendBytes(emch, rc.SGRMouse(0, 5, 3, true)); err != nil {
		t.Fatalf("send-text (SGR release): %v", err)
	}
	waitFor(t, 10*time.Second, "injected SGR bytes echoed by cat -v", func() bool {
		plain, err := client.GetText(rc.GetTextOpts{Match: emch})
		return err == nil && strings.Contains(plain, "[<0;5;3M") && strings.Contains(plain, "[<0;5;3m")
	})
	t.Logf("raw delivery: SGR press+release bytes (%q) arrived verbatim on the child PTY", report)

	// --- btop reacts to injected input: keyboard first (menu overlay via
	//     send-key), then an attributable SGR click.
	if err := client.SendKey(match, "escape"); err != nil {
		t.Fatalf("send-key escape: %v", err)
	}
	// the Esc menu overlay draws the block-art btop logo; braille graphs
	// and gauge fills never produce that glyph run
	const logoArt = "██████╗"
	var menuPlain string
	waitFor(t, 10*time.Second, "btop menu overlay after send-key escape", func() bool {
		menuPlain, err = client.GetText(rc.GetTextOpts{Match: match})
		return err == nil && strings.Contains(menuPlain, logoArt)
	})
	t.Logf("send-key: escape changed %d/%d lines (menu overlay)", diffLines(plain1, menuPlain), st.Rows)
	bankFixture(t, "spike1-btop-menu.txt", []byte(menuPlain))
	if err := client.SendKey(match, "escape"); err != nil {
		t.Fatalf("send-key escape (close): %v", err)
	}
	pollUntil(4*time.Second, func() bool {
		p, gErr := client.GetText(rc.GetTextOpts{Match: match})
		return gErr == nil && !strings.Contains(p, logoArt)
	})

	// attributable SGR click: btop's cpu box border renders an update-rate
	// spinner button "- 2000ms +"; clicking the + bumps it to 2100ms.
	// Churn never rewrites that label, so the change is the click's.
	const spinner = "- 2000ms +"
	plainPre, err := client.GetText(rc.GetTextOpts{Match: match})
	if err != nil {
		t.Fatalf("get-text before click: %v", err)
	}
	row, col := locate(plainPre, spinner)
	if row < 0 {
		t.Fatalf("btop border has no %q spinner; layout assumption broken:\n%s", spinner, plainPre)
	}
	bankFixture(t, "spike1-btop-click-before.ansi", []byte(mustGetANSI(t, client, match)))
	x, y := col+utf8.RuneCountInString(spinner), row+1 // 1-based cell of the +
	if err := client.SendBytes(match, rc.SGRMouse(0, x, y, false)); err != nil {
		t.Fatalf("send-text click press: %v", err)
	}
	if err := client.SendBytes(match, rc.SGRMouse(0, x, y, true)); err != nil {
		t.Fatalf("send-text click release: %v", err)
	}
	if pollUntil(6*time.Second, func() bool {
		p, gErr := client.GetText(rc.GetTextOpts{Match: match})
		return gErr == nil && strings.Contains(p, "2100ms")
	}) {
		t.Logf("SGR click at (%d,%d) hit the + button: update rate 2000ms -> 2100ms", x, y)
	} else {
		// fallback probe: the "menu" border button opens the logo overlay
		mrow, mcol := locate(plainPre, "┐menu┌")
		if mrow >= 0 {
			mx, my := mcol+3, mrow+1 // the 'e' cell, 1-based
			_ = client.SendBytes(match, rc.SGRMouse(0, mx, my, false))
			_ = client.SendBytes(match, rc.SGRMouse(0, mx, my, true))
			if pollUntil(6*time.Second, func() bool {
				p, gErr := client.GetText(rc.GetTextOpts{Match: match})
				return gErr == nil && strings.Contains(p, logoArt)
			}) {
				t.Logf("SGR click at (%d,%d) opened the menu (fallback button)", mx, my)
			} else {
				t.Errorf("SGR click: neither + spinner (%d,%d) nor menu button (%d,%d) reacted", x, y, mx, my)
			}
		} else {
			t.Errorf("SGR click: + spinner at (%d,%d) did not react and no menu button found", x, y)
		}
	}
	bankFixture(t, "spike1-btop-click-after.ansi", []byte(mustGetANSI(t, client, match)))

	// --- Release tears both down
	for _, s := range []*WidgetState{st, est} {
		if err := sup.Release(ctx, s); err != nil {
			t.Errorf("Release(%s): %v", s.Widget.ID, err)
		}
		if s.WindowID != 0 {
			t.Errorf("Release(%s): window id not cleared", s.Widget.ID)
		}
	}
	tree, err = client.LS()
	if err != nil {
		t.Fatalf("ls after release: %v", err)
	}
	if _, ok := rc.FindWindowByUserVar(tree, UserVarWidget, btopWidget.ID); ok {
		t.Errorf("Release: btop window still present in ls")
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	if !pollUntil(timeout, ok) {
		t.Fatalf("timed out after %s waiting for %s", timeout, what)
	}
}

func pollUntil(timeout time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

func mustGetANSI(t *testing.T, client *rc.Client, match string) string {
	t.Helper()
	s, err := client.GetText(rc.GetTextOpts{Match: match, ANSI: true})
	if err != nil {
		t.Fatalf("get-text --ansi %s: %v", match, err)
	}
	return s
}

// osWindowOf returns the os-window containing kitty window id.
func osWindowOf(tree []rc.OSWindow, id int) (rc.OSWindow, bool) {
	for _, osw := range tree {
		for _, tab := range osw.Tabs {
			for _, w := range tab.Windows {
				if w.ID == id {
					return osw, true
				}
			}
		}
	}
	return rc.OSWindow{}, false
}

// locate returns the 0-based row and rune column of needle's first
// occurrence on the screen, or -1,-1.
func locate(screen, needle string) (row, col int) {
	for i, l := range splitScreen(screen) {
		if j := strings.Index(l, needle); j >= 0 {
			return i, utf8.RuneCountInString(l[:j])
		}
	}
	return -1, -1
}

// countSGRParam counts SGR sequences carrying param as a standalone
// semicolon-separated token (colon subparams stay glued to their base).
func countSGRParam(s, param string) int {
	n := 0
	for _, m := range regexp.MustCompile(`\x1b\[([0-9:;]*)m`).FindAllStringSubmatch(s, -1) {
		for tok := range strings.SplitSeq(m[1], ";") {
			if tok == param {
				n++
				break
			}
		}
	}
	return n
}

// scrapeOnce runs one Scraper.Poll and blocks for the sink callback.
func scrapeOnce(t *testing.T, sc Scraper, st *WidgetState) string {
	t.Helper()
	type result struct {
		snap []byte
		err  error
	}
	ch := make(chan result, 1)
	sc.Poll(st, func(id string, snapshot []byte, err error) {
		ch <- result{snapshot, err}
	})
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("scrape: %v", r.err)
		}
		return string(r.snap)
	case <-time.After(10 * time.Second):
		t.Fatal("scrape: sink never called")
		return ""
	}
}

func splitScreen(s string) []string {
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func nonBlankLines(s string) int {
	n := 0
	for _, l := range splitScreen(s) {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

func diffLines(a, b string) int {
	al, bl := splitScreen(a), splitScreen(b)
	n := max(len(al), len(bl))
	changed := 0
	for i := range n {
		var av, bv string
		if i < len(al) {
			av = al[i]
		}
		if i < len(bl) {
			bv = bl[i]
		}
		if av != bv {
			changed++
		}
	}
	return changed
}

// bankFixture writes a live capture into testdata for offline consumers.
func bankFixture(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatalf("bank %s: %v", name, err)
	}
	path := filepath.Join("testdata", name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("bank %s: %v", name, err)
	}
	t.Logf("banked %s (%d bytes)", path, len(data))
}
