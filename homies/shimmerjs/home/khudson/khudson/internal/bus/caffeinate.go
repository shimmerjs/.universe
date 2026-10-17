// The bus holds display/idle assertions only while an Edge view is available and
// the user preference is on. One goroutine owns spawn/kill. caffeinate -w
// also ties the assertion to the bus PID when normal cleanup cannot run.
// The pidfile supports recovery of older children created without -w.
package bus

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/atomicfile"
	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/subprocess"
)

const (
	caffBin = "/usr/bin/caffeinate"
	// restart backoff
	caffBackoffMin     = time.Second
	caffBackoffMax     = 30 * time.Second
	caffHealthyAfter   = time.Minute
	caffPidFileName    = "caffeinate.pid"
	caffStateOn        = "on"
	caffStateOff       = "off"
	caffStateOnStarted = "on (starting)" // desired on, process not up yet
)

// caffProc is one running caffeinate; the exec seam fakes stand in for in
// tests (no live process in unit tests).
type caffProc interface {
	Pid() int
	Kill()       // best-effort terminate; Wait reaps
	Wait() error // blocks until exit
}

type realCaffProc struct{ cmd *exec.Cmd }

func (p realCaffProc) Pid() int { return p.cmd.Process.Pid }
func (p realCaffProc) Kill() {
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		diagnostic.Default.Observe("caffeinate.kill", err, "wait_for_exit_before_replacement")
	}
}
func (p realCaffProc) Wait() error { return p.cmd.Wait() }

// launchCaffeinate starts the real assertion holder: -d (display) + -i
// (idle system sleep), held until the process dies.
func launchCaffeinate() (caffProc, error) {
	cmd := caffeinateCommand(os.Getpid())
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return realCaffProc{cmd: cmd}, nil
}

func caffeinateCommand(ownerPID int) *exec.Cmd {
	return exec.Command(caffBin, "-di", "-w", strconv.Itoa(ownerPID))
}

// caffIsCaffeinate reports whether pid is (still) a caffeinate process, via
// ps comm. Read failures remain errors so recovery keeps the owner record.
func caffIsCaffeinate(pid int) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := subprocess.Output(ctx, "/bin/ps", "-o", "comm=", "-p", strconv.Itoa(pid))
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		return false, err
	}
	comm := strings.TrimSpace(string(out))
	return comm == caffBin || strings.HasSuffix(comm, "/caffeinate") || comm == "caffeinate", nil
}

func caffSigterm(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

// caffeinator supervises the caffeinate child. Its mutex is a leaf (like
// mainKittyHealth): safe to touch under b.mu, never takes b.mu itself.
type caffeinator struct {
	pidFile string
	// exec seams
	launch func() (caffProc, error)
	isCaff func(pid int) (bool, error) // orphan identity check
	kill   func(pid int) error

	minBackoff, maxBackoff time.Duration
	healthyAfter           time.Duration

	mu     sync.Mutex
	paused bool          // no Edge view can use the assertion
	want   bool          // desired state; the loop converges the process onto it
	pid    int           // 0 = no live process
	kick   chan struct{} // buffered(1): wakes the loop after a toggle
}

func newCaffeinator(on bool, p paths.Paths) *caffeinator {
	return &caffeinator{
		pidFile:      filepath.Join(p.Dir, caffPidFileName),
		launch:       launchCaffeinate,
		isCaff:       caffIsCaffeinate,
		kill:         caffSigterm,
		minBackoff:   caffBackoffMin,
		maxBackoff:   caffBackoffMax,
		healthyAfter: caffHealthyAfter,
		want:         on,
		paused:       true,
		kick:         make(chan struct{}, 1),
	}
}

func (c *caffeinator) SetAvailable(available bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.paused = !available
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func (c *caffeinator) active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.want && !c.paused
}

// Set flips the desired state and wakes the loop. State moves immediately
// (the broadcast reflects intent); the loop converges the process.
func (c *caffeinator) Set(on bool) {
	c.mu.Lock()
	c.want = on
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default: // a kick is already pending; the loop re-reads want anyway
	}
}

// On is the desired state. Nil-safe for bare test buses.
func (c *caffeinator) On() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.want
}

// wire is the broadcast/greeting value: desired state only.
func (c *caffeinator) wire() string {
	if c.On() {
		return caffStateOn
	}
	return caffStateOff
}

// State is the ctl status value; unlike wire it surfaces the gap between
// desired and running ("on (starting)" while a spawn or backoff is pending).
func (c *caffeinator) State() string {
	if c == nil {
		return caffStateOff
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case !c.want:
		return caffStateOff
	case c.paused:
		return "on (waiting for Edge view)"
	case c.pid != 0:
		return caffStateOn
	default:
		return caffStateOnStarted
	}
}

func (c *caffeinator) setPid(pid int) {
	c.mu.Lock()
	c.pid = pid
	c.mu.Unlock()
}

func (c *caffeinator) writePid(pid int) {
	if c.pidFile == "" {
		return
	}
	if err := atomicfile.Write(c.pidFile, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		diagnostic.Default.Observe("caffeinate.owner.write", err, "continue_with_parent_pid_guard")
	}
}

func (c *caffeinator) dropPid() {
	if c.pidFile == "" {
		return
	}
	if err := os.Remove(c.pidFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		diagnostic.Default.Observe("caffeinate.owner.remove", err, "reverify_on_restart")
	}
}

// reapOrphan recovers from a SIGKILLed previous bus (see the package comment
// for why pidfile-scoped, not a process sweep).
func (c *caffeinator) reapOrphan() error {
	if c.pidFile == "" {
		return nil
	}
	data, err := os.ReadFile(c.pidFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("invalid caffeinate owner file %s", c.pidFile)
	}
	ours, err := c.isCaff(pid)
	if err != nil {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			c.dropPid()
			return nil
		}
		return fmt.Errorf("cannot verify caffeinate pid %d: %w", pid, err)
	}
	if !ours {
		c.dropPid()
		return nil
	}
	if err := c.kill(pid); err != nil {
		return fmt.Errorf("orphan caffeinate pid %d: %w", pid, err)
	}
	c.dropPid()
	log.Printf("khudson bus: terminated orphan caffeinate pid %d", pid)
	return nil
}

// run is the supervision loop; the ONLY goroutine that spawns or kills, so
// two copies cannot race into existence. Blocks until ctx is done, killing
// any live process on the way out.
func (c *caffeinator) run(ctx context.Context) {
	retry := c.minBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.reapOrphan()
		diagnostic.Default.Observe("caffeinate.orphan", err, "retry_without_spawning_on_error")
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
		retry = min(retry*2, c.maxBackoff)
	}
	if !c.On() {
		log.Printf("khudson bus: caffeinate off at start (config)")
	}

	var (
		proc    caffProc
		died    chan error // nil while no process: blocks the select arm
		spawned time.Time
		backoff = c.minBackoff
		retryAt time.Time
	)
	stop := func(why string) {
		if proc == nil {
			return
		}
		proc.Kill()
		<-died
		proc, died = nil, nil
		c.setPid(0)
		c.dropPid()
		log.Printf("khudson bus: caffeinate off (%s)", why)
	}

	for {
		if ctx.Err() != nil {
			stop("bus shutdown")
			return
		}
		if c.active() {
			if proc == nil && !time.Now().Before(retryAt) {
				p, err := c.launch()
				diagnostic.Default.Observe("caffeinate.spawn", err, "backoff_on_error")
				if err != nil {
					log.Printf("khudson bus: caffeinate spawn: %v; retry in %s", err, backoff)
					retryAt = time.Now().Add(backoff)
					backoff = min(backoff*2, c.maxBackoff)
				} else {
					proc = p
					died = make(chan error, 1)
					go func(p caffProc) { died <- p.Wait() }(p)
					spawned = time.Now()
					// pidfile before the state flips on: once status reads
					// on, orphan recovery already covers this pid
					c.writePid(p.Pid())
					c.setPid(p.Pid())
					log.Printf("khudson bus: caffeinate on (pid %d, %s -di)", p.Pid(), caffBin)
				}
			}
		} else {
			stop("disabled or no dock")
			retryAt = time.Time{}
		}

		// arm a retry timer only while waiting out a backoff
		var timer *time.Timer
		var retry <-chan time.Time
		if c.active() && proc == nil {
			timer = time.NewTimer(max(time.Until(retryAt), 0))
			retry = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			stop("bus shutdown")
			return
		case err := <-died:
			proc, died = nil, nil
			c.setPid(0)
			c.dropPid()
			if time.Since(spawned) >= c.healthyAfter {
				backoff = c.minBackoff
			}
			log.Printf("khudson bus: caffeinate died (%v); restart in %s", err, backoff)
			retryAt = time.Now().Add(backoff)
			backoff = min(backoff*2, c.maxBackoff)
		case <-c.kick:
		case <-retry:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// setCaffeinate is the toggle verb body (ctl and dock-initiated alike):
// mutate desired state and broadcast it under one b.mu acquisition so
// interleaved toggles cannot reorder on the wire (the theme pattern). The
// TypeCaffeinate broadcast is the dock's ack.
func (b *Bus) setCaffeinate(arg string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var on bool
	switch arg {
	case "on":
		on = true
	case "off":
		on = false
	case "toggle":
		on = !b.caff.On()
	default:
		return "", errors.New("caffeinate " + strconv.Quote(arg) + " is not on|off|toggle")
	}
	b.caff.Set(on)
	state := b.caff.wire()
	b.broadcastLocked(proto.Msg{Type: proto.TypeCaffeinate, Caffeinate: state})
	return state, nil
}
