// Caffeinate supervision: khudson owns the machine's background caffeinate.
// The bus spawns /usr/bin/caffeinate -di (display + idle assertions), default
// ON at start (config caffeinate.on), restarts it with backoff if it dies,
// and kills it on toggle-off and bus shutdown. One supervision goroutine owns
// spawn/kill, so a second copy is impossible by construction (single-flight);
// toggles only flip the desired state and kick the loop. Desired state is the
// surfaced/broadcast state -- spawn latency and crash-restarts are supervisor
// detail the widget must not flap on; ctl status distinguishes "on (starting)"
// for honesty.
//
// Orphan handling: launchctl kickstart -k SIGKILLs the bus, which skips the
// kill and leaves the child caffeinate reparented to launchd, holding the
// assertions forever. Every spawn writes the child pid to caffeinate.pid in
// the state dir; the next bus reads it at startup and SIGTERMs that pid --
// after verifying via ps that it still names a caffeinate, so pid reuse never
// kills an innocent process. This is the simplest honest recovery: a
// pgrep-style sweep could kill a caffeinate the user started by hand, while
// the pidfile scopes the kill to processes this supervisor created.
package bus

import (
	"errors"
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

	"context"

	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
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

func (p realCaffProc) Pid() int    { return p.cmd.Process.Pid }
func (p realCaffProc) Kill()       { _ = p.cmd.Process.Kill() }
func (p realCaffProc) Wait() error { return p.cmd.Wait() }

// launchCaffeinate starts the real assertion holder: -d (display) + -i
// (idle system sleep), held until the process dies.
func launchCaffeinate() (caffProc, error) {
	cmd := exec.Command(caffBin, "-di")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return realCaffProc{cmd: cmd}, nil
}

// caffIsCaffeinate reports whether pid is (still) a caffeinate process, via
// ps comm. Any failure -- pid gone, ps missing -- reports false: never kill
// blind.
func caffIsCaffeinate(pid int) bool {
	out, err := exec.Command("/bin/ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(string(out))
	return comm == caffBin || strings.HasSuffix(comm, "/caffeinate") || comm == "caffeinate"
}

func caffSigterm(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

// caffeinator supervises the caffeinate child. Its mutex is a leaf (like
// mainKittyHealth): safe to touch under b.mu, never takes b.mu itself.
type caffeinator struct {
	pidFile string
	// exec seams
	launch func() (caffProc, error)
	isCaff func(pid int) bool // orphan identity check
	kill   func(pid int) error

	minBackoff, maxBackoff time.Duration
	healthyAfter           time.Duration

	mu   sync.Mutex
	want bool          // desired state; the loop converges the process onto it
	pid  int           // 0 = no live process
	kick chan struct{} // buffered(1): wakes the loop after a toggle
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
		kick:         make(chan struct{}, 1),
	}
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
	if err := os.WriteFile(c.pidFile, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		log.Printf("khudson bus: caffeinate pidfile: %v", err)
	}
}

func (c *caffeinator) dropPid() {
	if c.pidFile == "" {
		return
	}
	if err := os.Remove(c.pidFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("khudson bus: caffeinate pidfile: %v", err)
	}
}

// reapOrphan recovers from a SIGKILLed previous bus (see the package comment
// for why pidfile-scoped, not a process sweep).
func (c *caffeinator) reapOrphan() {
	if c.pidFile == "" {
		return
	}
	data, err := os.ReadFile(c.pidFile)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("khudson bus: caffeinate pidfile: %v", err)
		}
		return
	}
	defer c.dropPid()
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		log.Printf("khudson bus: caffeinate pidfile corrupt (%q); ignoring", data)
		return
	}
	if !c.isCaff(pid) {
		return // exited on its own, or the pid was reused: hands off
	}
	if err := c.kill(pid); err != nil {
		log.Printf("khudson bus: orphaned caffeinate pid %d: kill: %v", pid, err)
		return
	}
	log.Printf("khudson bus: killed orphaned caffeinate (pid %d) left by a previous bus", pid)
}

// run is the supervision loop; the ONLY goroutine that spawns or kills, so
// two copies cannot race into existence. Blocks until ctx is done, killing
// any live process on the way out.
func (c *caffeinator) run(ctx context.Context) {
	c.reapOrphan()
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
		if c.On() {
			if proc == nil && !time.Now().Before(retryAt) {
				p, err := c.launch()
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
			stop("toggled off")
			retryAt = time.Time{}
		}

		// arm a retry timer only while waiting out a backoff
		var timer *time.Timer
		var retry <-chan time.Time
		if c.On() && proc == nil {
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
