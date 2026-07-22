package bus

import (
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// Effect exec: the second half of module.EffectHandler. Effects are
// module-built argvs from vetted sources (reno: the loaded script's
// closed command registry), and the dispatch site enforces its own
// closed shape on top (effectArgvAllowed) -- this path must never grow
// into a second wire-reachable exec route. Unlike row-act children,
// effect children mutate the machine mid-demo (kitty/OBS/filesystem),
// so they run ORDERED (one worker, FIFO chains: a step's launch must
// finish before its focus resolves) under process-group tracking and
// die at shutdown -- a hung demo wrapper surviving a restart would act
// against the next run. The orphan window after a SIGKILL (kickstart -k)
// is accepted and bounded instead of pidfile-reaped: every reno verb
// self-terminates on its own deadline (runKitten 10s, renoVerbDeadline
// in the CLI), so nothing long-lived can leak.

// effectKillGrace is how long shutdown waits between the group SIGTERM
// and the SIGKILL sweep.
const effectKillGrace = 2 * time.Second

// effectChain is one transition's ordered effects.
type effectChain struct {
	source  string
	effects []module.Effect
	// ack is the flush marker's rendezvous; nil on real chains.
	ack chan struct{}
}

// effectRunner owns effect execution: one worker goroutine draining a
// FIFO of chains (order is the contract), the per-key debounce, and the
// live child's process group for shutdown kill.
type effectRunner struct {
	queue chan effectChain

	mu     sync.Mutex
	last   map[string]time.Time // Effect.Key -> dispatch time
	group  int                  // live child pgid; 0 = none
	closed bool
	done   chan struct{}
	now    func() time.Time
}

func newEffectRunner() *effectRunner {
	return &effectRunner{
		queue: make(chan effectChain, 16),
		last:  make(map[string]time.Time),
		done:  make(chan struct{}),
		now:   time.Now,
	}
}

// dispatchEffects hands a transition's effects to the ordered worker.
// Non-blocking: a full queue drops the CHAIN loudly (the input worker
// must never stall behind a wedged effect), and drops surface exactly
// like refused acts so the stage sees them.
func (b *Bus) dispatchEffects(source string, effects []module.Effect) {
	if len(effects) == 0 {
		return
	}
	if b.effects == nil {
		log.Printf("khudson bus: %s: no effect runner; dropped %d effects", source, len(effects))
		return
	}
	for _, e := range effects {
		if !effectArgvAllowed(e.Argv) {
			log.Printf("khudson bus: %s: effect argv refused (not a reno verb shape): %v", source, e.Argv)
			b.broadcast(proto.Msg{Type: proto.TypeNotice,
				Error: fmt.Sprintf("%s: effect refused (not a reno verb)", source)})
			b.actFailed(e.Argv, fmt.Errorf("refused (not a reno verb shape)"))
			return
		}
	}
	select {
	case b.effects.queue <- effectChain{source: source, effects: effects}:
	default:
		log.Printf("khudson bus: %s: effect queue full; dropped %d effects", source, len(effects))
		b.broadcast(proto.Msg{Type: proto.TypeNotice,
			Error: fmt.Sprintf("%s: effect queue full, step actions dropped", source)})
	}
}

// effectArgvAllowed pins the executor's argv shape: `khudson reno
// <verb> ...` (argv[0] bare khudson or an absolute path whose base is
// khudson -- the os.Executable TCC identity). The script vet is the
// semantic gate; this is the structural one at the dispatch site, so a
// buggy or foreign EffectHandler cannot turn the executor into a
// general exec service.
func effectArgvAllowed(argv []string) bool {
	if len(argv) < 3 {
		return false
	}
	head := argv[0]
	if head != "khudson" {
		if !filepath.IsAbs(head) || !strings.HasPrefix(filepath.Base(head), "khudson") {
			return false
		}
	}
	return argv[1] == "reno"
}

// run is the ordered worker: chains FIFO, effects within a chain
// strictly sequential (start, WAIT, next) -- a step's launch completes
// before its focus resolves, and two fast transitions never interleave
// their machine actions. Failures are loud per effect and do not stop
// the rest of the chain: half a step is better than none mid-show, and
// the events journal records exactly which action died.
func (b *Bus) runEffects() {
	r := b.effects
	defer close(r.done)
	for chain := range r.queue {
		if chain.ack != nil {
			close(chain.ack)
			continue
		}
		for _, e := range chain.effects {
			if r.debounce(e.Key) {
				log.Printf("khudson bus: %s: debounced repeat effect %s: %v", chain.source, e.Key, e.Argv)
				b.broadcast(proto.Msg{Type: proto.TypeNotice,
					Error: fmt.Sprintf("%s: repeat effect dropped (%s)", chain.source, e.Key)})
				continue
			}
			wait, err := b.startEffect(e.Argv)
			if err != nil {
				log.Printf("khudson bus: %s effect %v: %v", chain.source, e.Argv, err)
				b.broadcast(proto.Msg{Type: proto.TypeNotice,
					Error: fmt.Sprintf("%s effect failed to start: %v", chain.source, err)})
				b.actFailed(e.Argv, err)
				continue
			}
			log.Printf("khudson bus: %s effect %s: %v", chain.source, e.Key, e.Argv)
			if err := wait(); err != nil {
				log.Printf("khudson bus: %s effect %v: %v", chain.source, e.Argv, err)
				b.broadcast(proto.Msg{Type: proto.TypeNotice,
					Error: fmt.Sprintf("%s effect %v: %v", chain.source, filepath.Base(e.Argv[0]), err)})
				b.actFailed(e.Argv, err)
			}
			r.clearGroup()
		}
	}
}

// debounce reports whether key fired within the window; a pass records
// it. Sweep-first bounds the map; drops do not refresh the window
// (debounceRepeat semantics, under the runner's lock).
func (r *effectRunner) debounce(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return debounceRepeat(r.last, key, r.now())
}

// startEffect starts one effect child in its own process group so
// shutdown can kill the whole tree. The execStart test seam (when set)
// wins: seam-driven tests observe argvs without real processes.
func (b *Bus) startEffect(argv []string) (wait func() error, err error) {
	if b.execStart != nil {
		return b.execStart(argv)
	}
	r := b.effects
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("bus shutting down")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	r.group = cmd.Process.Pid // Setpgid: child's pgid == its pid
	r.mu.Unlock()
	return cmd.Wait, nil
}

func (r *effectRunner) clearGroup() {
	r.mu.Lock()
	r.group = 0
	r.mu.Unlock()
}

// flush waits until every previously queued chain has drained -- a test
// rendezvous, not a runtime facility.
func (r *effectRunner) flush() {
	ack := make(chan struct{})
	r.queue <- effectChain{ack: ack}
	<-ack
}

// shutdown stops the worker and terminates a live effect group:
// SIGTERM, grace, SIGKILL. New starts are refused from here on.
func (r *effectRunner) shutdown() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	group := r.group
	r.mu.Unlock()
	if group != 0 {
		_ = syscall.Kill(-group, syscall.SIGTERM)
	}
	close(r.queue)
	select {
	case <-r.done:
		return
	case <-time.After(effectKillGrace):
	}
	r.mu.Lock()
	if r.group != 0 {
		log.Printf("khudson bus: effect group %d survived SIGTERM; killing", r.group)
		_ = syscall.Kill(-r.group, syscall.SIGKILL)
	}
	r.mu.Unlock()
	<-r.done
}
