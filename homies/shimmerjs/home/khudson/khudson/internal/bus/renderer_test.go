package bus

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

type fakeRenderer struct {
	done  chan error
	stops int
}

func TestStatusDuringRendererReplacement(t *testing.T) {
	b, _, _ := schedTestBus(t)
	var workers sync.WaitGroup
	workers.Go(func() {
		for range 100 {
			b.installRenderer("/unused-status-test.sock")
		}
	})
	workers.Go(func() {
		for range 100 {
			if reply := b.handleCtl(proto.Msg{Cmd: "status"}); !reply.OK {
				t.Errorf("status: %s", reply.Error)
			}
		}
	})
	workers.Wait()
}

func TestRendererOrphanRecoveryRetainsUnresolvedOwnership(t *testing.T) {
	for _, scenario := range []string{"probe denied", "leader gone group alive", "kill denied", "kill ineffective", "gone", "pid reused", "term exits", "kill exits"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			ownerPath := filepath.Join(dir, "renderer-owner.json")
			owner := rendererOwner{PID: 42, Socket: filepath.Join(dir, "r-42-1.sock")}
			data, _ := json.Marshal(owner)
			if err := os.WriteFile(ownerPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			gone := scenario == "gone"
			var signals []syscall.Signal
			ops := rendererReapOps{
				pause: func(time.Duration) {},
				command: func(int) (string, error) {
					if scenario == "pid reused" {
						return "/unrelated/program", nil
					}
					return "kitty --listen-on unix:" + owner.Socket, nil
				},
				signal: func(pid int, sig syscall.Signal) error {
					if gone {
						return syscall.ESRCH
					}
					if sig == 0 {
						if scenario == "probe denied" {
							return syscall.EPERM
						}
						if scenario == "leader gone group alive" && pid > 0 {
							return syscall.ESRCH
						}
						return nil
					}
					if pid != -owner.PID {
						t.Fatalf("signal targeted PID %d", pid)
					}
					signals = append(signals, sig)
					if scenario == "kill denied" {
						return syscall.EPERM
					}
					if scenario == "term exits" || (scenario == "kill exits" && sig == syscall.SIGKILL) {
						gone = true
					}
					return nil
				},
			}
			err := reapRendererWith(ownerPath, dir, ops)
			wantFailure := scenario == "probe denied" || scenario == "leader gone group alive" || scenario == "kill denied" || scenario == "kill ineffective"
			if (err != nil) != wantFailure {
				t.Fatalf("reap error=%v, wantFailure=%v", err, wantFailure)
			}
			_, statErr := os.Stat(ownerPath)
			if wantFailure && statErr != nil {
				t.Fatalf("lost unresolved owner: %v", statErr)
			}
			if !wantFailure && !os.IsNotExist(statErr) {
				t.Fatalf("resolved owner remains: %v", statErr)
			}
			if (scenario == "probe denied" || scenario == "leader gone group alive" || scenario == "pid reused" || scenario == "gone") && len(signals) != 0 {
				t.Fatalf("unverified owner signaled: %v", signals)
			}
			if scenario == "kill exits" && (len(signals) != 2 || signals[1] != syscall.SIGKILL) {
				t.Fatalf("missing escalation: %v", signals)
			}
		})
	}
}

func (p *fakeRenderer) Done() <-chan error { return p.done }
func (p *fakeRenderer) Stop() {
	p.stops++
	select {
	case p.done <- nil:
	default:
	}
}
func TestRendererDemandAndGenerationIsolation(t *testing.T) {
	b, _, _ := schedTestBus(t)
	var launched []string
	var children []*fakeRenderer
	b.renderer = &rendererLifecycle{dir: t.TempDir(), cleanup: func() {}, launch: func(socket string) (rendererProcess, error) {
		p := &fakeRenderer{done: make(chan error, 1)}
		children = append(children, p)
		launched = append(launched, socket)
		return p, nil
	}}
	now := time.Now()
	if b.stepRenderer(now) || len(launched) != 0 {
		t.Fatal("renderer started without dock")
	}
	addFakeDock(t, b)
	if !b.stepRenderer(now) || len(launched) != 1 {
		t.Fatal("renderer did not start")
	}
	old, _ := b.reg.Get("w")
	old.setWindowID(1)
	b.mu.Lock()
	clear(b.docks)
	b.mu.Unlock()
	if !b.stepRenderer(now) || children[0].stops != 1 {
		t.Fatal("renderer did not stop")
	}
	addFakeDock(t, b)
	b.stepRenderer(now)
	if len(launched) != 2 || launched[0] == launched[1] {
		t.Fatal("reused endpoint")
	}
	fresh, _ := b.reg.Get("w")
	fresh.setWindowID(1)
	b.applySnapshot(snapshotResult{state: old, id: "w", win: 1, data: []byte("old")}, map[string]*schedEntry{})
	if len(fresh.Snapshot) != 0 {
		t.Fatal("old generation snapshot accepted")
	}
}
func TestRendererFailureBackoffAndNativeOnlyLayout(t *testing.T) {
	b, _, _ := schedTestBus(t)
	addFakeDock(t, b)
	calls := 0
	b.renderer = &rendererLifecycle{dir: t.TempDir(), cleanup: func() {}, launch: func(string) (rendererProcess, error) { calls++; return nil, errors.New("no renderer") }}
	now := time.Now()
	for i := range 8 {
		b.stepRenderer(now.Add(time.Duration(i) * 250 * time.Millisecond))
	}
	if calls != 1 {
		t.Fatalf("spawn attempts=%d", calls)
	}
	b.mu.Lock()
	w := b.cfg.Widgets["w"]
	w.Render = config.Render{Kind: "native"}
	b.cfg.Widgets["w"] = w
	b.mu.Unlock()
	b.stepRenderer(now.Add(time.Minute))
	if calls != 1 {
		t.Fatal("native layout spawned renderer")
	}
}
func TestRendererArgsKeepSocketOnlyAndHideMouseActions(t *testing.T) {
	args := strings.Join(rendererArgs("/tmp/test.sock"), " ")
	for _, want := range []string{"allow_remote_control=socket-only", "clear_all_mouse_actions=yes", "--listen-on unix:/tmp/test.sock", "--start-as hidden"} {
		if !strings.Contains(args, want) {
			t.Fatal(args)
		}
	}
}

func TestRendererQuiescesBeforeReplacingCrashedGeneration(t *testing.T) {
	b, _, _ := schedTestBus(t)
	addFakeDock(t, b)
	var child *fakeRenderer
	starts := 0
	b.renderer = &rendererLifecycle{dir: t.TempDir(), cleanup: func() {}, launch: func(string) (rendererProcess, error) {
		starts++
		child = &fakeRenderer{done: make(chan error, 1)}
		return child, nil
	}}
	now := time.Now()
	b.stepRenderer(now)
	child.done <- errors.New("crashed")
	if !b.rendererTransition(now) {
		t.Fatal("crash did not quiesce scheduler")
	}
	entries := map[string]*schedEntry{"w": {busy: true}}
	if schedulerIdle(entries) {
		t.Fatal("in-flight work considered idle")
	}
	entries["w"].busy = false
	b.stepRenderer(now)
	if starts != 1 || b.rendererSocket != "" {
		t.Fatal("crash did not enter backoff")
	}
	b.stepRenderer(now.Add(2 * time.Second))
	if starts != 2 {
		t.Fatal("restart not attempted")
	}
}

func TestReloadWithoutManagedRendererDoesNotArmAdoption(t *testing.T) {
	b, _, _ := schedTestBus(t)
	b.renderer = &rendererLifecycle{}
	b.pending = b.cfg
	b.trySwapPending(map[string]*schedEntry{})
	if b.adoptPending() {
		t.Fatal("reload armed RC work without renderer")
	}
}
