package bus

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// fakeEffectMod is a native module whose "fx:go" act is handled with
// effects.
type fakeEffectMod struct {
	handled [][]string
	effects []module.Effect
}

func (*fakeEffectMod) Name() string { return "fake-fx" }
func (*fakeEffectMod) Poll(context.Context, map[string]any) (module.Data, error) {
	return module.Data{}, nil
}
func (f *fakeEffectMod) HandleActEffects(argv []string) (bool, []module.Effect) {
	if argv[0] != "fx:go" {
		return false, nil
	}
	f.handled = append(f.handled, argv)
	return true, f.effects
}

// execRecorder is a mutex-guarded start log: the effect worker runs on
// its own goroutine, so the seam must be race-clean.
type execRecorder struct {
	mu     sync.Mutex
	starts [][]string
}

func (r *execRecorder) start(argv []string) (func() error, error) {
	r.mu.Lock()
	r.starts = append(r.starts, argv)
	r.mu.Unlock()
	return func() error { return nil }, nil
}

func (r *execRecorder) all() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.starts)
}

func effectTestBus(t *testing.T) (*Bus, *fakeEffectMod, *execRecorder) {
	t.Helper()
	b, _ := inputTestBus()
	mod := &fakeEffectMod{}
	b.cfg.Widgets["n"] = config.Widget{ID: "n",
		Render: config.Render{Kind: "native", Module: "fake-fx"}}
	b.reg = NewRegistry(b.cfg)
	b.mods = map[string]module.Module{"fake-fx": mod}
	b.repoll = make(chan string, 4)
	b.effects = newEffectRunner()
	go b.runEffects()
	t.Cleanup(b.effects.shutdown)
	rec := &execRecorder{}
	b.execStart = rec.start
	st, _ := b.reg.Get("n")
	st.setActs([][]string{{"fx:go"}})
	return b, mod, rec
}

// A vetted act handled with effects moves in-process state, pokes a
// repoll, and execs each effect exactly once per key window, in order.
func TestHandleRowActEffectDispatch(t *testing.T) {
	b, mod, rec := effectTestBus(t)
	mod.effects = []module.Effect{
		{Key: "run/0/1/0", Argv: []string{"khudson", "reno", "launch-session", "work"}},
		{Key: "run/0/1/1", Argv: []string{"khudson", "reno", "focus-session", "work"}},
	}

	// unpublished argv never reaches the effector
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n", Argv: []string{"fx:go", "crafted"}})
	if len(mod.handled) != 0 {
		t.Fatalf("unvetted argv reached the module: %v", mod.handled)
	}

	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n", Argv: []string{"fx:go"}})
	if len(mod.handled) != 1 {
		t.Fatal("act not handled")
	}
	b.effects.flush()
	starts := rec.all()
	if len(starts) != 2 || starts[0][2] != "launch-session" || starts[1][2] != "focus-session" {
		t.Fatalf("effects must exec in script order: %v", starts)
	}
	select {
	case id := <-b.repoll:
		if id != "n" {
			t.Fatalf("repoll = %q", id)
		}
	default:
		t.Fatal("effectful act sent no repoll poke")
	}

	// identical keys inside the window are debounced (retap replay);
	// fresh keys (new visit) pass despite identical argvs
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n", Argv: []string{"fx:go"}})
	b.effects.flush()
	if len(rec.all()) != 2 {
		t.Fatalf("repeat keys must debounce, execs = %d", len(rec.all()))
	}
	mod.effects = []module.Effect{{Key: "run/0/2/0", Argv: []string{"khudson", "reno", "focus-session", "work"}}}
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n", Argv: []string{"fx:go"}})
	b.effects.flush()
	if len(rec.all()) != 3 {
		t.Fatalf("fresh key must exec despite identical argv, execs = %d", len(rec.all()))
	}

	// the window expires: same key fires again
	b.effects.now = func() time.Time { return time.Now().Add(rowActDebounce + time.Second) }
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n", Argv: []string{"fx:go"}})
	b.effects.flush()
	if len(rec.all()) != 4 {
		t.Fatalf("expired key must exec, execs = %d", len(rec.all()))
	}
}

// The dispatch site enforces the reno verb shape: anything else is
// refused before it reaches the worker, however it got returned.
func TestEffectArgvGate(t *testing.T) {
	b, mod, rec := effectTestBus(t)
	mod.effects = []module.Effect{
		{Key: "k1", Argv: []string{"rm", "-rf", "/tmp/x"}},
	}
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "n", Argv: []string{"fx:go"}})
	b.effects.flush()
	if len(rec.all()) != 0 {
		t.Fatalf("non-reno argv must be refused: %v", rec.all())
	}

	cases := []struct {
		argv []string
		ok   bool
	}{
		{[]string{"khudson", "reno", "focus-session", "w"}, true},
		{[]string{"/inst/path/khudson", "reno", "focus-app", "com.x"}, true},
		{[]string{"khudson", "claude", "focus", "sid"}, false},
		{[]string{"relative/khudson", "reno", "open-url", "https://x"}, false},
		{[]string{"khudson", "reno"}, false},
		{[]string{"open", "https://x"}, false},
	}
	for _, tc := range cases {
		if got := effectArgvAllowed(tc.argv); got != tc.ok {
			t.Errorf("effectArgvAllowed(%v) = %v, want %v", tc.argv, got, tc.ok)
		}
	}
}

// Effect children run in their own process group and die at shutdown.
func TestEffectShutdownKillsGroups(t *testing.T) {
	b := &Bus{effects: newEffectRunner()}
	go b.runEffects()
	wait, err := b.startEffect([]string{"sleep", "30"})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = wait() }()

	b.effects.mu.Lock()
	pgid := b.effects.group
	b.effects.mu.Unlock()
	if pgid == 0 {
		t.Fatal("child not tracked")
	}

	done := make(chan struct{})
	go func() { b.effects.shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(effectKillGrace + 3*time.Second):
		t.Fatal("shutdown hung")
	}
	// SIGTERM delivery is asynchronous; poll briefly for the group's death
	dead := false
	for range 20 {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			dead = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !dead {
		t.Fatalf("effect group %d still alive", pgid)
	}
	if _, err := b.startEffect([]string{"sleep", "30"}); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("post-shutdown start must refuse, got %v", err)
	}
}

// fakeRenoCtl stands in for the reno module behind the ctl seam.
type fakeRenoCtl struct {
	got     [][]string
	data    any
	effects []module.Effect
	err     error
}

func (*fakeRenoCtl) Name() string { return "reno" }
func (*fakeRenoCtl) Poll(context.Context, map[string]any) (module.Data, error) {
	return module.Data{}, nil
}
func (f *fakeRenoCtl) HandleCtl(args []string) (any, []module.Effect, error) {
	f.got = append(f.got, args)
	return f.data, f.effects, f.err
}

func TestHandleCtlReno(t *testing.T) {
	b, _, rec := effectTestBus(t)
	f := &fakeRenoCtl{
		data:    map[string]string{"run": "r1"},
		effects: []module.Effect{{Key: "r1/0/1/0", Argv: []string{"khudson", "reno", "open-url", "https://x"}}},
	}
	b.mods["reno"] = f

	resp := b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"next"}})
	if !resp.OK {
		t.Fatalf("resp = %+v", resp)
	}
	if len(f.got) != 1 || f.got[0][0] != "next" {
		t.Fatalf("ctl args = %v", f.got)
	}
	b.effects.flush()
	if len(rec.all()) != 1 {
		t.Fatalf("ctl-driven effects must exec, starts = %v", rec.all())
	}
	var data map[string]string
	if err := json.Unmarshal(resp.Data, &data); err != nil || data["run"] != "r1" {
		t.Fatalf("data = %s (%v)", resp.Data, err)
	}

	f.err = errors.New("no live run")
	resp = b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"stop"}})
	if resp.OK || !strings.Contains(resp.Error, "no live run") {
		t.Fatalf("error resp = %+v", resp)
	}

	delete(b.mods, "reno")
	resp = b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"status"}})
	if resp.OK || !strings.Contains(resp.Error, "not registered") {
		t.Fatalf("absent module resp = %+v", resp)
	}
}
