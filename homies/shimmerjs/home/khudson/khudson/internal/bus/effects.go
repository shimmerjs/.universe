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
// finish before its focus resolves) under process-group tracking, die
// at clean shutdown, and are cut at run boundaries (module.BoundaryKey:
// run A's queued chains must not execute into run B). The orphan window
// after a SIGKILL (kickstart -k) is accepted and bounded instead of
// pidfile-reaped: every reno verb self-terminates on its own deadline
// (runKitten 10s, renoVerbDeadline in the CLI), so nothing long-lived
// can leak.

// effectKillGrace is how long shutdown waits between the group SIGTERM
// and the SIGKILL sweep.
const effectKillGrace = 2 * time.Second

// effectChain is one transition's ordered effects, stamped with the
// boundary epoch it was enqueued under.
type effectChain struct {
	source  string
	effects []module.Effect
	epoch   int
	// ack is the flush marker's rendezvous; nil on real chains.
	ack chan struct{}
}

// effectRunner owns effect execution: one worker goroutine draining a
// FIFO of chains (order is the contract), the per-key debounce, the
// boundary epoch, and the live child's process group. The queue is
// NEVER closed -- producers live on both the input worker and ctl
// connections, and shutdown cannot quiesce them; the worker exits via
// stopCh and late dispatches are refused on the closed flag.
type effectRunner struct {
	queue  chan effectChain
	stopCh chan struct{}

	// dispatchMu serializes {module transition -> dispatch} as ONE unit
	// across producers (input worker acts, ctl conns): without it a
	// boundary from producer B can bump the epoch between producer A's
	// cursor move and A's enqueue, stamping run A's chain with run B's
	// epoch -- the exact leak the boundary exists to stop (arc-2 review
	// #15).
	dispatchMu sync.Mutex

	mu         sync.Mutex
	last       map[string]time.Time // Effect.Key -> dispatch time
	group      int                  // live child pgid; 0 = none
	groupFinal bool                 // live child runs a Final (cleanup) effect
	epoch      int                  // bumped at run boundaries
	closed     bool
	done       chan struct{}
	now        func() time.Time
}

func newEffectRunner() *effectRunner {
	return &effectRunner{
		queue:  make(chan effectChain, 16),
		stopCh: make(chan struct{}),
		last:   make(map[string]time.Time),
		done:   make(chan struct{}),
		now:    time.Now,
	}
}

// dispatchEffects hands a transition's effects to the ordered worker.
// A leading module.BoundaryKey effect cuts the stream FIRST: queued
// chains from before the boundary are invalidated (epoch bump) and the
// live child group gets SIGTERM -- stop/start/load must not let run A's
// staged actions execute into run B. Non-blocking enqueue: a full queue
// drops the chain loudly (the input worker must never stall behind a
// wedged effect).
func (b *Bus) dispatchEffects(source string, effects []module.Effect) {
	if len(effects) == 0 {
		return
	}
	if b.effects == nil {
		log.Printf("khudson bus: %s: no effect runner; dropped %d effects", source, len(effects))
		return
	}
	rest := make([]module.Effect, 0, len(effects))
	for _, e := range effects {
		if e.Key == module.BoundaryKey && len(e.Argv) == 0 {
			b.effects.boundary(source)
			continue
		}
		if !effectArgvAllowed(e.Argv) {
			log.Printf("khudson bus: %s: effect argv refused (not a reno verb shape): %v", source, e.Argv)
			b.broadcast(proto.Msg{Type: proto.TypeNotice,
				Error: fmt.Sprintf("%s: effect refused (not a reno verb)", source)})
			b.actFailed(e.Argv, fmt.Errorf("refused (not a reno verb shape)"))
			return
		}
		rest = append(rest, e)
	}
	if len(rest) == 0 {
		return
	}
	r := b.effects
	r.mu.Lock()
	closed, epoch := r.closed, r.epoch
	r.mu.Unlock()
	if closed {
		log.Printf("khudson bus: %s: shutting down; dropped %d effects", source, len(rest))
		return
	}
	chain := effectChain{source: source, effects: rest, epoch: epoch}
	select {
	case r.queue <- chain:
		return
	default:
	}
	if chainHasFinal(rest) {
		// Final effects are the recording's stop path: dropping one
		// finalizes a run while OBS records unowned (arc-2 review #2).
		// A bounded blocking send trades a short producer stall for
		// guaranteed cleanup; each queued chain is verb-deadline-bounded,
		// so a live queue drains well inside the wait.
		select {
		case r.queue <- chain:
			return
		case <-time.After(finalEnqueueWait):
			log.Printf("khudson bus: %s: effect queue wedged %s; FINAL cleanup dropped -- stop the recording by hand (khudson reno obs stop)", source, finalEnqueueWait)
			b.broadcast(proto.Msg{Type: proto.TypeNotice,
				Error: fmt.Sprintf("%s: cleanup dropped; run `khudson reno obs stop`", source)})
			return
		}
	}
	log.Printf("khudson bus: %s: effect queue full; dropped %d effects", source, len(rest))
	b.broadcast(proto.Msg{Type: proto.TypeNotice,
		Error: fmt.Sprintf("%s: effect queue full, step actions dropped", source)})
}

// finalEnqueueWait bounds the blocking send for Final-bearing chains.
const finalEnqueueWait = 5 * time.Second

func chainHasFinal(effects []module.Effect) bool {
	for _, e := range effects {
		if e.Final {
			return true
		}
	}
	return false
}

// DispatchTransition runs one module transition and its effect dispatch
// as an atomic unit under dispatchMu -- every effect producer must route
// through here (see the field comment).
func (b *Bus) DispatchTransition(source string, fn func() []module.Effect) {
	if b.effects != nil {
		b.effects.dispatchMu.Lock()
		defer b.effects.dispatchMu.Unlock()
	}
	b.dispatchEffects(source, fn())
}

// boundary invalidates every queued chain and interrupts the live
// child: the run that enqueued them is over. A live FINAL child is
// exempt -- it IS the cleanup a boundary must not cancel (a quick
// stop-then-start would otherwise SIGTERM the in-flight recording stop,
// arc-2 review #16).
func (r *effectRunner) boundary(source string) {
	r.mu.Lock()
	r.epoch++
	group, final := r.group, r.groupFinal
	r.mu.Unlock()
	log.Printf("khudson bus: %s: effect boundary (queued chains invalidated)", source)
	if group != 0 && !final {
		_ = syscall.Kill(-group, syscall.SIGTERM)
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

// runEffects is the ordered worker: chains FIFO, effects within a chain
// strictly sequential (start, WAIT, next) -- a step's launch completes
// before its focus resolves, and two fast transitions never interleave
// their machine actions. Stale-epoch chains (enqueued before a run
// boundary) drop loudly. Failures are loud per effect and do not stop
// the rest of the chain: half a step is better than none mid-show, and
// the events journal records exactly which action died.
func (b *Bus) runEffects() {
	r := b.effects
	defer close(r.done)
	for {
		var chain effectChain
		select {
		case <-r.stopCh:
			return
		case chain = <-r.queue:
		}
		if chain.ack != nil {
			close(chain.ack)
			continue
		}
		for _, e := range chain.effects {
			// re-check the boundary epoch PER EFFECT, not per chain: a
			// stop landing mid-chain must drop the remainder of the
			// invalidated step, not just future chains. Final effects
			// (the recording stop) are cleanup and survive boundaries.
			r.mu.Lock()
			stale := chain.epoch != r.epoch
			r.mu.Unlock()
			if stale && !e.Final {
				log.Printf("khudson bus: %s: dropped effect %s from a finished run: %v", chain.source, e.Key, e.Argv)
				continue
			}
			if r.debounce(e.Key) {
				log.Printf("khudson bus: %s: debounced repeat effect %s: %v", chain.source, e.Key, e.Argv)
				b.broadcast(proto.Msg{Type: proto.TypeNotice,
					Error: fmt.Sprintf("%s: repeat effect dropped (%s)", chain.source, e.Key)})
				continue
			}
			wait, err := b.startEffect(e.Argv, e.Final)
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
// shutdown can kill the whole tree; final marks a cleanup child the
// boundary kill must spare. The execStart test seam (when set) wins:
// seam-driven tests observe argvs without real processes.
func (b *Bus) startEffect(argv []string, final bool) (wait func() error, err error) {
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
	r.groupFinal = final
	r.mu.Unlock()
	return cmd.Wait, nil
}

func (r *effectRunner) clearGroup() {
	r.mu.Lock()
	r.group = 0
	r.groupFinal = false
	r.mu.Unlock()
}

// flush waits until every previously queued chain has drained -- a test
// rendezvous, not a runtime facility.
func (r *effectRunner) flush() {
	ack := make(chan struct{})
	r.queue <- effectChain{ack: ack}
	<-ack
}

// shutdown refuses new work, terminates a live effect group (SIGTERM,
// grace, SIGKILL), and stops the worker. The queue stays open forever:
// producers (input worker, ctl conns) are never quiesced, and a send on
// a closed channel would panic the bus at the worst moment.
func (r *effectRunner) shutdown() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		<-r.done
		return
	}
	r.closed = true
	group := r.group
	r.mu.Unlock()
	if group != 0 {
		_ = syscall.Kill(-group, syscall.SIGTERM)
	}
	close(r.stopCh)
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
