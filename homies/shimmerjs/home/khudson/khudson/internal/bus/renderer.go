package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/atomicfile"
	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/rc"
	"github.com/shimmerjs/khudson/khudson/internal/subprocess"
)

type rendererProcess interface {
	Stop()
	Done() <-chan error
}
type rendererChild struct {
	cmd  *exec.Cmd
	done chan error
}

func (p *rendererChild) Done() <-chan error { return p.done }
func (p *rendererChild) Stop() {
	signalRenderer(p.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		signalRenderer(p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
	}
	signalRenderer(p.cmd.Process.Pid, syscall.SIGKILL)
}
func signalRenderer(pid int, signal syscall.Signal) {
	err := syscall.Kill(-pid, signal)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		diagnostic.Default.Observe("renderer.signal", fmt.Errorf("signal %d: %w", signal, err), "wait_for_exit_before_replacement")
	}
}
func rendererArgs(socket string) []string {
	return []string{"--config", "NONE", "-o", "clear_all_mouse_actions=yes", "-o", "allow_remote_control=socket-only", "-o", "macos_hide_from_tasks=yes", "--listen-on", "unix:" + socket, "--title", "khudson-substrate", "--start-as", "hidden"}
}

type rendererOwner struct {
	PID    int    `json:"pid"`
	Socket string `json:"socket"`
}

func launchRenderer(bin, socket, ownerPath string) (rendererProcess, error) {
	cmd := exec.Command(bin, rendererArgs(socket)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &rendererChild{cmd: cmd, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait(); close(p.done) }()
	data, _ := json.Marshal(rendererOwner{PID: cmd.Process.Pid, Socket: socket})
	if err := atomicfile.Write(ownerPath, data, 0600); err != nil {
		p.Stop()
		return nil, err
	}
	return p, nil
}

// Refuse a new renderer if its predecessor's identity cannot be established.
// Never sweep by process name. The retained owner file makes retry possible.
func reapRenderer(ownerPath, dir string) error {
	return reapRendererWith(ownerPath, dir, rendererReapOps{
		signal: syscall.Kill,
		pause:  time.Sleep,
		command: func(pid int) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			out, err := subprocess.Output(ctx, "/bin/ps", "-o", "command=", "-p", strconv.Itoa(pid))
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return string(out), err
		},
	})
}

type rendererReapOps struct {
	signal  func(int, syscall.Signal) error
	command func(int) (string, error)
	pause   func(time.Duration)
}

func reapRendererWith(ownerPath, dir string, ops rendererReapOps) error {
	data, err := os.ReadFile(ownerPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var owner rendererOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return fmt.Errorf("invalid renderer owner file: %w", err)
	}
	if owner.PID <= 1 || filepath.Dir(owner.Socket) != dir || !strings.HasPrefix(filepath.Base(owner.Socket), "r-") {
		return errors.New("invalid renderer owner identity")
	}
	if err := ops.signal(owner.PID, 0); err != nil {
		if !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("probe renderer owner: %w", err)
		}
		if err := ops.signal(-owner.PID, 0); !errors.Is(err, syscall.ESRCH) {
			if err != nil {
				return fmt.Errorf("probe orphan renderer group: %w", err)
			}
			return errors.New("renderer group remains without a verifiable leader")
		}
	} else {
		out, err := ops.command(owner.PID)
		if err != nil {
			return fmt.Errorf("verify renderer owner: %w", err)
		}
		if !strings.Contains(out, "unix:"+owner.Socket) {
			return os.Remove(ownerPath)
		}
		for _, stage := range []struct {
			signal syscall.Signal
			probes int
		}{
			{syscall.SIGTERM, 100}, {syscall.SIGKILL, 20},
		} {
			if err := ops.signal(-owner.PID, stage.signal); errors.Is(err, syscall.ESRCH) {
				break
			} else if err != nil {
				return fmt.Errorf("terminate renderer group: %w", err)
			}
			gone := false
			for range stage.probes {
				err := ops.signal(-owner.PID, 0)
				if errors.Is(err, syscall.ESRCH) {
					gone = true
					break
				}
				if err != nil {
					return fmt.Errorf("await renderer group: %w", err)
				}
				ops.pause(50 * time.Millisecond)
			}
			if gone {
				break
			}
		}
		if err := ops.signal(-owner.PID, 0); !errors.Is(err, syscall.ESRCH) {
			if err != nil {
				return fmt.Errorf("confirm renderer group exit: %w", err)
			}
			return errors.New("renderer group did not exit after SIGKILL")
		}
	}
	_ = os.Remove(owner.Socket)
	return os.Remove(ownerPath)
}

type rendererLifecycle struct {
	launch   func(string) (rendererProcess, error)
	cleanup  func()
	dir      string
	proc     rendererProcess
	socket   string
	serial   uint64
	failures int
	retry    time.Time
	started  time.Time
	exited   bool
	exitErr  error
}

func (b *Bus) rendererWanted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id := range b.visibleWidgetsLocked() {
		if b.cfg.Widgets[id].Render.Kind == "exec" {
			return true
		}
	}
	return false
}

// Called only after scheduler work has quiesced. Each generation has a
// distinct socket and registry; late input and results retain the old target.
func (b *Bus) stepRenderer(now time.Time) bool {
	r := b.renderer
	if r == nil {
		return false
	}
	changed := false
	b.rendererTransition(now)
	if r.exited {
		r.proc.Stop()
		r.proc = nil
		r.exited = false
		if now.Sub(r.started) >= time.Minute {
			r.failures = 0
		}
		r.failures++
		r.retry = now.Add(backoffFor(r.failures))
		if r.exitErr == nil {
			r.exitErr = errors.New("renderer exited before demand ended")
		}
		diagnostic.Default.Observe("renderer.exit", r.exitErr, "backoff")
		changed = true
	}
	want := b.rendererWanted()
	if !want && r.proc != nil {
		r.proc.Stop()
		r.proc = nil
		changed = true
		diagnostic.OperationGeneration("renderer.lifecycle", "", r.serial, "idle", "stop after demand withdrawn")
	}
	if changed {
		r.cleanup()
		_ = os.Remove(r.socket)
		b.installRenderer("")
	}
	if want && r.proc == nil && !now.Before(r.retry) {
		r.serial++
		socket := filepath.Join(r.dir, fmt.Sprintf("r-%d-%d.sock", os.Getpid(), r.serial))
		proc, err := r.launch(socket)
		if err != nil {
			r.failures++
			r.retry = now.Add(backoffFor(r.failures))
			diagnostic.Default.Observe("renderer.spawn", err, "backoff")
		} else {
			diagnostic.Default.Observe("renderer.spawn", nil, "resume")
			r.proc, r.socket, r.started = proc, socket, now
			b.installRenderer(socket)
			changed = true
			diagnostic.OperationGeneration("renderer.lifecycle", "", r.serial, "started", socket)
		}
	}
	return changed
}
func (b *Bus) installRenderer(socket string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	next := NewRegistry(b.cfg)
	for _, id := range b.reg.IDs() {
		st, _ := b.reg.Get(id)
		if st.Widget.Render.Kind != "exec" {
			next.widgets[id] = st
		}
	}
	b.reg = next
	b.inputEpoch.Add(1)
	b.invalidateViewsLocked()
	b.broadcastLocked(proto.Msg{Type: proto.TypeViewState})
	b.needAdopt = socket != ""
	client := rc.New(socket)
	b.substrateRC = client
	b.sup = NewSupervisor(client)
	b.scrape = NewScraper(client)
	b.inj = NewInjector(client)
	b.rendererSocket = socket
}
func (b *Bus) stopRenderer() {
	if b.renderer == nil {
		return
	}
	if b.renderer.proc != nil {
		b.renderer.proc.Stop()
		b.renderer.proc = nil
		b.renderer.cleanup()
		_ = os.Remove(b.renderer.socket)
	}
}
func schedulerIdle(entries map[string]*schedEntry) bool {
	for _, e := range entries {
		if e.busy {
			return false
		}
	}
	return true
}

func (b *Bus) rendererTransition(now time.Time) bool {
	r := b.renderer
	if r == nil {
		return false
	}
	if r.proc != nil && !r.exited {
		select {
		case r.exitErr = <-r.proc.Done():
			r.exited = true
		default:
		}
	}
	want := b.rendererWanted()
	return r.exited || (r.proc != nil && !want) || (r.proc == nil && want && !now.Before(r.retry))
}
