package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

type fakeInj struct {
	mu   sync.Mutex
	sgr  []string
	keys []string
}

func (f *fakeInj) SendSGR(match string, button, x, y int, release bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sgr = append(f.sgr, fmt.Sprintf("%s b%d @%d,%d r=%v", match, button, x, y, release))
	return nil
}

func (f *fakeInj) SendKey(match string, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, fmt.Sprintf("%s %v", match, keys))
	return nil
}

func inputTestBus() (*Bus, *fakeInj) {
	cfg := &config.Config{
		Widgets: map[string]config.Widget{
			"w": {ID: "w",
				Render: config.Render{Kind: "exec", Argv: []string{"true"}},
				Gestures: map[string]config.Action{
					"swipe-left": {Verb: "send-key", Keys: "right", Target: "hud-window:w"},
					"swipe-up":   {Verb: "send-key", Keys: "x", Target: "main-kitty:oops"},
					"long-press": {Verb: "run", Argv: []string{"echo", "vetted"}},
				},
			},
		},
		Layouts: map[string]config.Layout{"main": {Tiles: []string{"w"}}},
		Layout:  "main",
	}
	inj := &fakeInj{}
	b := &Bus{cfg: cfg, reg: NewRegistry(cfg), inj: inj}
	st, _ := b.reg.Get("w")
	st.setWindowID(7)
	st.setSize(100, 30)
	return b, inj
}

func TestHandleForward(t *testing.T) {
	b, inj := inputTestBus()

	// tap: press+release, 0-based widget coords -> 1-based SGR
	b.handleForward(proto.Msg{Type: proto.TypeForward, Widget: "w",
		Gesture: &proto.Gesture{Kind: proto.GestureTap, Col: 4, Row: 2}})
	want := []string{"id:7 b0 @5,3 r=false", "id:7 b0 @5,3 r=true"}
	if len(inj.sgr) != 2 || inj.sgr[0] != want[0] || inj.sgr[1] != want[1] {
		t.Fatalf("tap injected %v, want %v", inj.sgr, want)
	}

	// out-of-range coords clamp to the widget grid
	inj.sgr = nil
	b.handleForward(proto.Msg{Type: proto.TypeForward, Widget: "w",
		Gesture: &proto.Gesture{Kind: proto.GestureTap, Col: 500, Row: -3}})
	if inj.sgr[0] != "id:7 b0 @100,1 r=false" {
		t.Fatalf("clamp failed: %v", inj.sgr[0])
	}

	// wheel: burst-capped press-only reports, button by direction
	inj.sgr = nil
	b.handleForward(proto.Msg{Type: proto.TypeForward, Widget: "w",
		Gesture: &proto.Gesture{Kind: proto.GestureWheel, Col: 0, Row: 0, DY: -9}})
	if len(inj.sgr) != wheelBurstCap {
		t.Fatalf("wheel burst = %d, want %d", len(inj.sgr), wheelBurstCap)
	}
	if inj.sgr[0] != "id:7 b64 @1,1 r=false" {
		t.Fatalf("wheel up wrong: %v", inj.sgr[0])
	}

	// horizontal wheel: DX sign picks the button (>0 right = 67, <0 left = 66)
	inj.sgr = nil
	b.handleForward(proto.Msg{Type: proto.TypeForward, Widget: "w",
		Gesture: &proto.Gesture{Kind: proto.GestureWheel, Col: 0, Row: 0, DX: 3}})
	if len(inj.sgr) != 3 || inj.sgr[0] != "id:7 b67 @1,1 r=false" {
		t.Fatalf("wheel right wrong: %v", inj.sgr)
	}
	inj.sgr = nil
	b.handleForward(proto.Msg{Type: proto.TypeForward, Widget: "w",
		Gesture: &proto.Gesture{Kind: proto.GestureWheel, Col: 0, Row: 0, DX: -2}})
	if len(inj.sgr) != 2 || inj.sgr[0] != "id:7 b66 @1,1 r=false" {
		t.Fatalf("wheel left wrong: %v", inj.sgr)
	}

	// mixed report: vertical loop first, then horizontal
	inj.sgr = nil
	b.handleForward(proto.Msg{Type: proto.TypeForward, Widget: "w",
		Gesture: &proto.Gesture{Kind: proto.GestureWheel, Col: 0, Row: 0, DX: 1, DY: -1}})
	want = []string{"id:7 b64 @1,1 r=false", "id:7 b67 @1,1 r=false"}
	if len(inj.sgr) != 2 || inj.sgr[0] != want[0] || inj.sgr[1] != want[1] {
		t.Fatalf("mixed wheel = %v, want %v", inj.sgr, want)
	}

	// unmaterialized widget: nothing injected
	inj.sgr = nil
	st, _ := b.reg.Get("w")
	st.setWindowID(0)
	b.handleForward(proto.Msg{Type: proto.TypeForward, Widget: "w",
		Gesture: &proto.Gesture{Kind: proto.GestureTap}})
	if len(inj.sgr) != 0 {
		t.Fatalf("injected into missing window: %v", inj.sgr)
	}
}

func TestHandleAction(t *testing.T) {
	b, inj := inputTestBus()

	// send-key resolves the hud-window target to its kitty window
	b.handleAction(proto.Msg{Type: proto.TypeAction, Widget: "w", Arg: "swipe-left"})
	if len(inj.keys) != 1 || inj.keys[0] != "id:7 [right]" {
		t.Fatalf("send-key injected %v", inj.keys)
	}

	// non-hud target is refused, not misrouted
	inj.keys = nil
	b.handleAction(proto.Msg{Type: proto.TypeAction, Widget: "w", Arg: "swipe-up"})
	if len(inj.keys) != 0 {
		t.Fatalf("main-kitty target leaked to hud injector: %v", inj.keys)
	}

	// unbound gesture is a no-op
	b.handleAction(proto.Msg{Type: proto.TypeAction, Widget: "w", Arg: "tap"})
	if len(inj.keys) != 0 {
		t.Fatalf("unbound gesture injected %v", inj.keys)
	}
}

// A row act only execs argv the bus itself published: the widget's last
// successful poll's acts or a config "run" gesture; wire-crafted argv is
// refused with no process start.
func TestHandleRowActRejectsForeignArgv(t *testing.T) {
	b, _ := inputTestBus()
	var starts [][]string
	b.execStart = func(argv []string) (func() error, error) {
		starts = append(starts, argv)
		return func() error { return nil }, nil
	}
	published := []string{"khudson", "claude", "focus", "abc"}
	st, _ := b.reg.Get("w")
	st.setActs([][]string{published})

	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "w",
		Argv: []string{"rm", "-rf", "/"}})
	if len(starts) != 0 {
		t.Fatalf("foreign argv exec'd: %v", starts)
	}

	// a published row act executes
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "w", Argv: published})
	if len(starts) != 1 || !slices.Equal(starts[0], published) {
		t.Fatalf("published act starts = %v, want [%v]", starts, published)
	}

	// the vetted-config leg: a run gesture's argv executes too
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "w",
		Argv: []string{"echo", "vetted"}})
	if len(starts) != 2 {
		t.Fatalf("config run argv starts = %v", starts)
	}
}

// fakeActMod is a native module claiming acts whose argv[0] is "mod:act".
type fakeActMod struct {
	handled [][]string
}

func (*fakeActMod) Name() string { return "fake-act" }
func (*fakeActMod) Poll(context.Context, map[string]any) (module.Data, error) {
	return module.Data{}, nil
}
func (f *fakeActMod) HandleAct(argv []string) bool {
	if argv[0] != "mod:act" {
		return false
	}
	f.handled = append(f.handled, argv)
	return true
}

// A vetted act on a native widget whose module implements ActHandler is
// handled in-process: no exec, and the widget gets a repoll poke. Argv the
// module declines still execs; the vet gate stays ahead of the dispatch.
func TestHandleRowActModuleDispatch(t *testing.T) {
	b, _ := inputTestBus()
	mod := &fakeActMod{}
	b.cfg.Widgets["n"] = config.Widget{ID: "n",
		Render: config.Render{Kind: "native", Module: "fake-act"}}
	b.reg = NewRegistry(b.cfg)
	b.mods = map[string]module.Module{"fake-act": mod}
	b.repoll = make(chan string, 1)
	var starts [][]string
	b.execStart = func(argv []string) (func() error, error) {
		starts = append(starts, argv)
		return func() error { return nil }, nil
	}
	handled := []string{"mod:act", "sid", "node"}
	declined := []string{"khudson", "claude", "focus", "abc"}
	st, _ := b.reg.Get("n")
	st.setActs([][]string{handled, declined})

	// unpublished argv never reaches the module, even with a matching verb
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n",
		Argv: []string{"mod:act", "crafted", "x"}})
	if len(mod.handled) != 0 {
		t.Fatalf("unvetted argv reached the module: %v", mod.handled)
	}

	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n", Argv: handled})
	if len(mod.handled) != 1 || len(starts) != 0 {
		t.Fatalf("handled act: module saw %v, exec saw %v; want in-process only", mod.handled, starts)
	}
	select {
	case id := <-b.repoll:
		if id != "n" {
			t.Fatalf("repoll poke = %q, want the widget id", id)
		}
	default:
		t.Fatal("handled act sent no repoll poke")
	}

	// declined argv falls through to exec, with no poke
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n", Argv: declined})
	if len(starts) != 1 || !slices.Equal(starts[0], declined) {
		t.Fatalf("declined act starts = %v, want the exec path", starts)
	}
	select {
	case id := <-b.repoll:
		t.Fatalf("declined act poked a repoll for %q", id)
	default:
	}
}

// The load-bearing publication chain, no hand-priming: a native poll's rows
// flow through applyNative into the act allowlist, and a tapped fold act
// dispatches in-process. A regression in the harvest loop must go red here,
// not surface as silent vet refusals on glass.
func TestApplyNativePublishesActsEndToEnd(t *testing.T) {
	b, _ := inputTestBus()
	mod := &fakeActMod{}
	b.cfg.Widgets["n"] = config.Widget{ID: "n",
		Render: config.Render{Kind: "native", Module: "fake-act"}}
	b.reg = NewRegistry(b.cfg)
	b.mods = map[string]module.Module{"fake-act": mod}
	b.repoll = make(chan string, 1)
	var starts [][]string
	b.execStart = func(argv []string) (func() error, error) {
		starts = append(starts, argv)
		return func() error { return nil }, nil
	}
	// step the clock past the debounce window per act: this test exercises
	// publication, not retap suppression
	clock := time.Now()
	b.actNow = func() time.Time { clock = clock.Add(rowActDebounce); return clock }

	fold := []string{"mod:act", "sid", "agents"}
	focus := []string{"khudson", "claude", "focus", "sid"}
	b.applyNative(nativeResult{id: "n", data: module.Data{Rows: []module.Row{
		{Kind: module.RowSpans, Act: fold},
		{Kind: module.RowSpans, Act: focus},
		{Kind: module.RowText, Text: "actless"},
	}}})

	// the harvested fold act dispatches in-process
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n", Argv: fold})
	if len(mod.handled) != 1 || len(starts) != 0 {
		t.Fatalf("fold act after applyNative: module saw %v, exec saw %v", mod.handled, starts)
	}
	// the harvested focus act execs
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n", Argv: focus})
	if len(starts) != 1 || !slices.Equal(starts[0], focus) {
		t.Fatalf("focus act after applyNative: starts = %v", starts)
	}
	// unpublished argv still refused after a real publication pass
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n",
		Argv: []string{"mod:act", "sid", "crafted"}})
	if len(mod.handled) != 1 {
		t.Fatalf("crafted argv reached the module after applyNative: %v", mod.handled)
	}
	// an error poll keeps the previous act set (the error branch never
	// clears the allowlist)
	b.applyNative(nativeResult{id: "n", err: context.DeadlineExceeded})
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n", Argv: fold})
	if len(mod.handled) != 2 {
		t.Fatal("error poll dropped the published act set")
	}
}

// Menu argvs ride the same publication chain as Act: the scheduler's
// applyNative harvests every row Menu item into st.Acts alongside the row
// act, so a tapped menu item passes vetRowAct and execs -- and an argv the
// poll never published (a crafted bundle id) is still refused.
func TestApplyNativePublishesMenuActs(t *testing.T) {
	b, _ := inputTestBus()
	b.cfg.Widgets["n"] = config.Widget{ID: "n",
		Render: config.Render{Kind: "native", Module: "fake-act"}}
	b.reg = NewRegistry(b.cfg)
	var starts [][]string
	b.execStart = func(argv []string) (func() error, error) {
		starts = append(starts, argv)
		return func() error { return nil }, nil
	}
	// step the clock past the debounce window per act: this test exercises
	// publication, not retap suppression
	clock := time.Now()
	b.actNow = func() time.Time { clock = clock.Add(rowActDebounce); return clock }

	open := []string{"open", "-a", "Safari"}
	quit := []string{"/inst/khudson", "ax", "quit", "--bundle", "com.apple.Safari"}
	fq := []string{"/inst/khudson", "ax", "force-quit", "--bundle", "com.apple.Safari"}
	b.applyNative(nativeResult{id: "n", data: module.Data{Rows: []module.Row{
		{Kind: module.RowText, Text: "Safari", Act: open, Menu: []module.Act{
			{Label: "Quit", Argv: quit},
			{Label: "Force Quit", Argv: fq, Destructive: true},
		}},
	}}})

	st, _ := b.reg.Get("n")
	if got := st.acts(); len(got) != 3 {
		t.Fatalf("published acts = %v, want the row act + both menu argvs", got)
	}

	// both menu argvs pass the vet and exec
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n", Argv: quit})
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n", Argv: fq})
	if len(starts) != 2 || !slices.Equal(starts[0], quit) || !slices.Equal(starts[1], fq) {
		t.Fatalf("menu act starts = %v, want quit then force-quit", starts)
	}

	// an unpublished argv -- same verb, crafted bundle -- is refused
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n",
		Argv: []string{"/inst/khudson", "ax", "force-quit", "--bundle", "com.evil.other"}})
	if len(starts) != 2 {
		t.Fatalf("unpublished menu argv exec'd: %v", starts)
	}
}

// The debounce decision table: an identical key within the window drops, a
// different key or an elapsed window passes, and a drop never stretches the
// window (it is measured from the last DISPATCH).
func TestDebounceRepeat(t *testing.T) {
	base := time.Now()
	keyA := rowActKey("dock-rail", []string{"open", "-a", "Xcode"})
	keyB := rowActKey("claude-panel", []string{"khudson", "claude", "focus", "468d7b14"})
	last := map[string]time.Time{}
	steps := []struct {
		key  string
		at   time.Duration
		drop bool
		why  string
	}{
		{keyA, 0, false, "first act passes"},
		{keyA, 500 * time.Millisecond, true, "identical retap within the window drops"},
		{keyB, 600 * time.Millisecond, false, "different (widget, argv) passes inside another act's window"},
		{keyA, 1900 * time.Millisecond, true, "still inside the window"},
		{keyA, 2 * time.Second, false, "window measured from the dispatch, not the last drop"},
		{keyA, 4100 * time.Millisecond, false, "window reopens after each dispatch"},
	}
	for _, s := range steps {
		if got := debounceRepeat(last, s.key, base.Add(s.at)); got != s.drop {
			t.Fatalf("%s: at %v drop = %v, want %v", s.why, s.at, got, s.drop)
		}
	}
	// the final call swept keyB (expired) and re-recorded keyA
	if len(last) != 1 {
		t.Fatalf("expired entries not swept: %d live", len(last))
	}
}

// The NUL join keeps a joined argv from aliasing a split one, and the widget
// is part of the key.
func TestRowActKeyDistinct(t *testing.T) {
	if rowActKey("w", []string{"open", "-a Xcode"}) == rowActKey("w", []string{"open", "-a", "Xcode"}) {
		t.Fatal("rowActKey collides across argv splits")
	}
	if rowActKey("w", []string{"x"}) == rowActKey("v", []string{"x"}) {
		t.Fatal("rowActKey ignores the widget")
	}
}

// A retap of the same vetted act within rowActDebounce is dropped at the
// dispatch seam -- on the exec path AND the in-process module path; a
// different argv or an elapsed window passes.
func TestHandleRowActDebouncesRetaps(t *testing.T) {
	b, _ := inputTestBus()
	mod := &fakeActMod{}
	b.cfg.Widgets["n"] = config.Widget{ID: "n",
		Render: config.Render{Kind: "native", Module: "fake-act"}}
	b.reg = NewRegistry(b.cfg)
	b.mods = map[string]module.Module{"fake-act": mod}
	b.repoll = make(chan string, 4)
	var starts [][]string
	b.execStart = func(argv []string) (func() error, error) {
		starts = append(starts, argv)
		return func() error { return nil }, nil
	}
	now := time.Now()
	b.actNow = func() time.Time { return now }

	execA := []string{"open", "-a", "Xcode"}
	execB := []string{"khudson", "claude", "focus", "468d7b14"}
	handled := []string{"mod:act", "sid", "node"}
	st, _ := b.reg.Get("n")
	st.setActs([][]string{execA, execB, handled})
	act := func(argv []string) {
		b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n", Argv: argv})
	}

	act(execA)
	act(execA) // identical retap within the window
	if len(starts) != 1 {
		t.Fatalf("retap exec'd: %d starts", len(starts))
	}
	act(execB) // different argv inside execA's window
	if len(starts) != 2 {
		t.Fatalf("distinct act debounced: %d starts", len(starts))
	}
	now = now.Add(rowActDebounce)
	act(execA) // window elapsed: dispatches again
	if len(starts) != 3 {
		t.Fatalf("act after the window debounced: %d starts", len(starts))
	}

	// the in-process path is NEVER debounced: fold acts are toggles whose
	// argv is identical in both directions, so an immediate retap must land
	act(handled)
	act(handled)
	if len(mod.handled) != 2 {
		t.Fatalf("in-process retap dropped: %v", mod.handled)
	}
	if got := len(b.repoll); got != 2 {
		t.Fatalf("repoll pokes = %d, want 2", got)
	}
}

// repollEntry dues the widget's poll now AND clears any failure backoff: a
// user tap must not wait out a stale backoff window.
func TestRepollEntryClearsSchedule(t *testing.T) {
	entries := map[string]*schedEntry{
		"n": {nextPoll: time.Now().Add(time.Minute), backoffUntil: time.Now().Add(30 * time.Second)},
	}
	repollEntry(entries, "n")
	if !entries["n"].nextPoll.IsZero() || !entries["n"].backoffUntil.IsZero() {
		t.Fatalf("entry after repoll = %+v, want zeroed schedule", entries["n"])
	}
	repollEntry(entries, "missing") // unknown id: no panic, no effect
}

// TestHelperProcess is the re-exec target for TestRowActSurfacesExit: the
// child exits 1 so the row-act waiter has a failure to surface.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	os.Exit(1)
}

// A row act that exits nonzero is surfaced: the waiter logs it and
// broadcasts a TypeNotice carrying the exit error.
func TestRowActSurfacesExit(t *testing.T) {
	b, _ := inputTestBus()
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	argv := []string{os.Args[0], "-test.run=TestHelperProcess"}
	st, _ := b.reg.Get("w")
	st.setActs([][]string{argv})

	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	b.docks = map[net.Conn]*json.Encoder{server: json.NewEncoder(server)}
	notices := make(chan proto.Msg, 1)
	go func() {
		dec := json.NewDecoder(client)
		for {
			var m proto.Msg
			if dec.Decode(&m) != nil {
				return
			}
			if m.Type == proto.TypeNotice {
				notices <- m
				return
			}
		}
	}()

	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "w", Argv: argv})

	select {
	case n := <-notices:
		if !strings.Contains(n.Error, "exit status 1") {
			t.Fatalf("notice %q does not carry the exit status", n.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no notice for the failed row act")
	}
}
