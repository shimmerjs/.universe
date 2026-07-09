package bus

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// fakeCaffProc is one fake caffeinate: Wait blocks until Kill (supervisor
// initiated) or die (crash) delivers the exit.
type fakeCaffProc struct {
	pid int

	mu     sync.Mutex
	dead   bool
	killed bool
	exit   chan error
}

func (p *fakeCaffProc) Pid() int { return p.pid }

func (p *fakeCaffProc) Wait() error { return <-p.exit }

func (p *fakeCaffProc) Kill() { p.terminate(errors.New("killed"), true) }

func (p *fakeCaffProc) die(err error) { p.terminate(err, false) }

func (p *fakeCaffProc) terminate(err error, killed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead {
		return
	}
	p.dead = true
	p.killed = killed
	p.exit <- err
}

func (p *fakeCaffProc) isDead() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead
}

func (p *fakeCaffProc) wasKilled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

// fakeLaunch is the exec seam: hands out fakeCaffProcs, records overlap (two
// live procs would mean the single-flight guarantee broke).
type fakeLaunch struct {
	mu         sync.Mutex
	errs       []error // consumed one per call before any spawn succeeds
	nextPid    int
	live       *fakeCaffProc
	overlapped bool
	spawns     chan *fakeCaffProc
}

func newFakeLaunch() *fakeLaunch {
	return &fakeLaunch{nextPid: 1000, spawns: make(chan *fakeCaffProc, 16)}
}

func (f *fakeLaunch) launch() (caffProc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return nil, err
	}
	if f.live != nil && !f.live.isDead() {
		f.overlapped = true
	}
	f.nextPid++
	p := &fakeCaffProc{pid: f.nextPid, exit: make(chan error, 1)}
	f.live = p
	f.spawns <- p
	return p, nil
}

func (f *fakeLaunch) sawOverlap() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.overlapped
}

func testCaffeinator(t *testing.T, on bool) (*caffeinator, *fakeLaunch) {
	t.Helper()
	fl := newFakeLaunch()
	return &caffeinator{
		pidFile:      filepath.Join(t.TempDir(), caffPidFileName),
		launch:       fl.launch,
		isCaff:       func(int) bool { return false },
		kill:         func(int) error { return nil },
		minBackoff:   time.Millisecond,
		maxBackoff:   20 * time.Millisecond,
		healthyAfter: time.Hour,
		want:         on,
		kick:         make(chan struct{}, 1),
	}, fl
}

// startCaff runs the supervision loop; cleanup cancels and waits it out.
func startCaff(t *testing.T, c *caffeinator) (cancel context.CancelFunc, done chan struct{}) {
	t.Helper()
	ctx, cancelCtx := context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() {
		defer close(done)
		c.run(ctx)
	}()
	t.Cleanup(func() {
		cancelCtx()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("caffeinator loop did not stop on ctx cancel")
		}
	})
	return cancelCtx, done
}

func wantSpawn(t *testing.T, fl *fakeLaunch) *fakeCaffProc {
	t.Helper()
	select {
	case p := <-fl.spawns:
		return p
	case <-time.After(2 * time.Second):
		t.Fatal("no caffeinate spawned within 2s")
		return nil
	}
}

func wantNoSpawn(t *testing.T, fl *fakeLaunch, within time.Duration) {
	t.Helper()
	select {
	case p := <-fl.spawns:
		t.Fatalf("unexpected caffeinate spawn (pid %d)", p.pid)
	case <-time.After(within):
	}
}

func waitCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s never happened", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// Default ON: the loop spawns at start, records the pidfile, and kills the
// child on shutdown (pidfile dropped: a killed child is no orphan).
func TestCaffeinateDefaultOnLifecycle(t *testing.T) {
	c, fl := testCaffeinator(t, true)
	cancel, done := startCaff(t, c)

	p := wantSpawn(t, fl)
	waitCond(t, "state on", func() bool { return c.State() == caffStateOn })
	data, err := os.ReadFile(c.pidFile)
	if err != nil {
		t.Fatalf("pidfile: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != strconv.Itoa(p.pid) {
		t.Fatalf("pidfile = %q, want %d", got, p.pid)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit on shutdown")
	}
	if !p.wasKilled() {
		t.Fatal("shutdown left the caffeinate running")
	}
	if _, err := os.Stat(c.pidFile); !os.IsNotExist(err) {
		t.Fatalf("pidfile after shutdown: %v, want removed", err)
	}
}

// Config default off: nothing spawns until a toggle asks for it.
func TestCaffeinateDefaultOffDoesNotSpawn(t *testing.T) {
	c, fl := testCaffeinator(t, false)
	startCaff(t, c)

	wantNoSpawn(t, fl, 100*time.Millisecond)
	if got := c.State(); got != caffStateOff {
		t.Fatalf("state = %q, want off", got)
	}
}

// A dead caffeinate is restarted (with backoff); the two processes never
// overlap.
func TestCaffeinateRestartsAfterDeath(t *testing.T) {
	c, fl := testCaffeinator(t, true)
	startCaff(t, c)

	p1 := wantSpawn(t, fl)
	p1.die(errors.New("crash"))
	p2 := wantSpawn(t, fl)
	if p2.pid == p1.pid {
		t.Fatal("restart handed back the dead proc")
	}
	if fl.sawOverlap() {
		t.Fatal("two caffeinates were live at once")
	}
}

// Spawn failures back off and retry until one lands.
func TestCaffeinateSpawnFailureRetries(t *testing.T) {
	c, fl := testCaffeinator(t, true)
	fl.errs = []error{errors.New("exec 1"), errors.New("exec 2")}
	startCaff(t, c)

	p := wantSpawn(t, fl)
	if p.pid == 0 {
		t.Fatal("no pid after retries")
	}
}

// Toggle round-trip at the supervisor: off kills and stays down, on respawns.
func TestCaffeinateToggleKillsAndRespawns(t *testing.T) {
	c, fl := testCaffeinator(t, true)
	startCaff(t, c)

	p1 := wantSpawn(t, fl)
	c.Set(false)
	waitCond(t, "toggle-off kill", func() bool { return p1.isDead() && p1.wasKilled() })
	waitCond(t, "state off", func() bool { return c.State() == caffStateOff })
	waitCond(t, "pidfile drop", func() bool {
		_, err := os.Stat(c.pidFile)
		return os.IsNotExist(err)
	})
	wantNoSpawn(t, fl, 50*time.Millisecond)

	c.Set(true)
	p2 := wantSpawn(t, fl)
	waitCond(t, "state on", func() bool { return c.State() == caffStateOn })
	if p2.pid == p1.pid {
		t.Fatal("toggle-on reused the killed proc")
	}
	if fl.sawOverlap() {
		t.Fatal("toggle produced overlapping caffeinates")
	}
}

// Rapid toggling never yields two live processes (single-flight: one loop
// owns spawn/kill).
func TestCaffeinateRapidTogglesSingleFlight(t *testing.T) {
	c, fl := testCaffeinator(t, true)
	startCaff(t, c)
	wantSpawn(t, fl)

	for i := range 20 {
		c.Set(i%2 == 0)
	}
	c.Set(true)
	waitCond(t, "converge on", func() bool { return c.State() == caffStateOn })
	if fl.sawOverlap() {
		t.Fatal("rapid toggles overlapped two caffeinates")
	}
}

func TestCaffeinatorStateStrings(t *testing.T) {
	for _, tt := range []struct {
		name        string
		c           *caffeinator
		state, wire string
	}{
		{name: "nil (bare test bus)", c: nil, state: "off", wire: "off"},
		{name: "off", c: &caffeinator{}, state: "off", wire: "off"},
		{name: "on running", c: &caffeinator{want: true, pid: 7}, state: "on", wire: "on"},
		{name: "on starting", c: &caffeinator{want: true}, state: "on (starting)", wire: "on"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.State(); got != tt.state {
				t.Errorf("State() = %q, want %q", got, tt.state)
			}
			if got := tt.c.wire(); got != tt.wire {
				t.Errorf("wire() = %q, want %q", got, tt.wire)
			}
		})
	}
}

// Orphan recovery: only a pidfile pid that still verifies as a caffeinate is
// killed; reused or vanished pids and corrupt files are left alone. The
// pidfile is consumed either way.
func TestCaffeinateReapOrphan(t *testing.T) {
	newC := func(t *testing.T, content string, isCaff bool) (*caffeinator, *[]int) {
		t.Helper()
		var killed []int
		c := &caffeinator{
			pidFile: filepath.Join(t.TempDir(), caffPidFileName),
			isCaff:  func(int) bool { return isCaff },
			kill: func(pid int) error {
				killed = append(killed, pid)
				return nil
			},
		}
		if content != "" {
			if err := os.WriteFile(c.pidFile, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return c, &killed
	}
	wantGone := func(t *testing.T, c *caffeinator) {
		t.Helper()
		if _, err := os.Stat(c.pidFile); !os.IsNotExist(err) {
			t.Fatalf("pidfile not consumed: %v", err)
		}
	}

	t.Run("verified orphan is killed", func(t *testing.T) {
		c, killed := newC(t, "4242", true)
		c.reapOrphan()
		if len(*killed) != 1 || (*killed)[0] != 4242 {
			t.Fatalf("killed = %v, want [4242]", *killed)
		}
		wantGone(t, c)
	})
	t.Run("reused pid untouched", func(t *testing.T) {
		c, killed := newC(t, "4242", false)
		c.reapOrphan()
		if len(*killed) != 0 {
			t.Fatalf("killed = %v, want none (pid no longer a caffeinate)", *killed)
		}
		wantGone(t, c)
	})
	t.Run("corrupt pidfile ignored", func(t *testing.T) {
		c, killed := newC(t, "not-a-pid", true)
		c.reapOrphan()
		if len(*killed) != 0 {
			t.Fatalf("killed = %v, want none", *killed)
		}
		wantGone(t, c)
	})
	t.Run("missing pidfile is a no-op", func(t *testing.T) {
		c, killed := newC(t, "", true)
		c.reapOrphan()
		if len(*killed) != 0 {
			t.Fatalf("killed = %v, want none", *killed)
		}
	})
}

// caffTestBus is a bus with a toggle-able caffeinator (no supervision loop:
// Set only flips desired state, which is exactly what the verb mutates).
func caffTestBus(t *testing.T, on bool) *Bus {
	t.Helper()
	cfg := &config.Config{
		Widgets: map[string]config.Widget{},
		Layouts: map[string]config.Layout{"main": {Kind: "dock-grid"}},
		Layout:  "main",
	}
	c, _ := testCaffeinator(t, on)
	return &Bus{
		cfg:   cfg,
		reg:   NewRegistry(cfg),
		docks: make(map[net.Conn]*json.Encoder),
		caff:  c,
	}
}

func wantCaffMsg(t *testing.T, ch <-chan proto.Msg) proto.Msg {
	t.Helper()
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				t.Fatal("dock connection closed before a caffeinate broadcast")
			}
			if m.Type == proto.TypeCaffeinate {
				return m
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no caffeinate broadcast within 2s")
		}
	}
}

// ctl caffeinate: every arg form mutates desired state, answers the new
// state, and broadcasts it so widgets re-render immediately.
func TestCtlCaffeinateRoundTrip(t *testing.T) {
	b := caffTestBus(t, true)
	ch := addDecodingDock(t, b)

	step := func(arg, want string) {
		t.Helper()
		resp := b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "caffeinate", Arg: arg})
		if !resp.OK {
			t.Fatalf("ctl caffeinate %s: %s", arg, resp.Error)
		}
		var data map[string]string
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			t.Fatalf("resp data: %v", err)
		}
		if data["caffeinate"] != want {
			t.Fatalf("resp state = %q, want %q", data["caffeinate"], want)
		}
		if m := wantCaffMsg(t, ch); m.Caffeinate != want {
			t.Fatalf("broadcast = %q, want %q", m.Caffeinate, want)
		}
	}
	step("toggle", "off")
	step("toggle", "on")
	step("off", "off")
	step("on", "on")

	resp := b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "caffeinate", Arg: "sideways"})
	if resp.OK || !strings.Contains(resp.Error, "on|off|toggle") {
		t.Fatalf("resp = %+v, want arg error", resp)
	}
}

// ctl status surfaces the supervisor state; a bare bus (nil caff) reads off.
func TestCaffeinateStatusSurface(t *testing.T) {
	status := func(t *testing.T, b *Bus) proto.Status {
		t.Helper()
		resp := b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "status"})
		if !resp.OK {
			t.Fatalf("ctl status: %s", resp.Error)
		}
		var st proto.Status
		if err := json.Unmarshal(resp.Data, &st); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		return st
	}

	b := caffTestBus(t, true)
	if st := status(t, b); st.Caffeinate != caffStateOnStarted {
		t.Fatalf("status.Caffeinate = %q, want %q (desired on, no process)", st.Caffeinate, caffStateOnStarted)
	}
	b.caff.setPid(7)
	if st := status(t, b); st.Caffeinate != caffStateOn {
		t.Fatalf("status.Caffeinate = %q, want on", st.Caffeinate)
	}
	b.caff.Set(false)
	if st := status(t, b); st.Caffeinate != caffStateOff {
		t.Fatalf("status.Caffeinate = %q, want off", st.Caffeinate)
	}

	bare := &Bus{cfg: b.cfg, reg: b.reg}
	if st := status(t, bare); st.Caffeinate != caffStateOff {
		t.Fatalf("bare bus status.Caffeinate = %q, want off", st.Caffeinate)
	}
}

// The dock role: greeting replays the caffeinate state, and a dock-initiated
// TypeCtl caffeinate toggle broadcasts the new state without a resp (the
// broadcast is the ack, exactly like layout nav).
func TestServeDockCaffeinateGreetingAndToggle(t *testing.T) {
	b := caffTestBus(t, true)

	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.serveDock(server, json.NewEncoder(server), json.NewDecoder(server),
			proto.Msg{Type: proto.TypeHello, Role: proto.RoleDock, Cols: 320, Rows: 18})
	}()
	msgs := make(chan proto.Msg, 16)
	go func() {
		dec := json.NewDecoder(client)
		for {
			var m proto.Msg
			if err := dec.Decode(&m); err != nil {
				close(msgs)
				return
			}
			msgs <- m
		}
	}()

	if m := wantCaffMsg(t, msgs); m.Caffeinate != "on" {
		t.Fatalf("greeting caffeinate = %q, want on", m.Caffeinate)
	}
	if err := json.NewEncoder(client).Encode(proto.Msg{
		Type: proto.TypeCtl, Cmd: "caffeinate", Arg: "toggle",
	}); err != nil {
		t.Fatal(err)
	}
	m := wantCaffMsg(t, msgs)
	if m.Caffeinate != "off" {
		t.Fatalf("toggle broadcast = %q, want off", m.Caffeinate)
	}
	if m.Type == proto.TypeResp {
		t.Fatal("dock toggle got a resp; the broadcast is the ack")
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveDock did not exit on close")
	}
}

// The real identity check execs ps; skip where the host lacks it. Never
// spawns a caffeinate: it only proves non-caffeinate pids read false.
func TestCaffIsCaffeinateRealPs(t *testing.T) {
	if _, err := os.Stat("/bin/ps"); err != nil {
		t.Skipf("no /bin/ps: %v", err)
	}
	if caffIsCaffeinate(os.Getpid()) {
		t.Error("test binary identified as caffeinate")
	}
	if caffIsCaffeinate(1 << 30) {
		t.Error("nonexistent pid identified as caffeinate")
	}
}
