// khudson is the khudson CLI; all subcommands (see usage) dispatch through
// stdlib flag.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/ax"
	"github.com/shimmerjs/khudson/khudson/internal/bus"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/dock"
	"github.com/shimmerjs/khudson/khudson/internal/hookspool"
	"github.com/shimmerjs/khudson/khudson/internal/hudlaunch"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/module/histsnap"
	"github.com/shimmerjs/khudson/khudson/internal/module/registry"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "bus":
		err = cmdBus(args[1:])
	case "dock":
		err = cmdDock(args[1:])
	case "ctl":
		err = cmdCtl(args[1:])
	case "hud-launcher":
		err = cmdHudLauncher(args[1:])
	case "claude":
		err = cmdClaude(args[1:])
	case "ax":
		err = cmdAx(args[1:])
	case "config":
		err = cmdConfig(args[1:])
	case "hook":
		err = cmdHook(args[1:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "khudson: unknown command %q\n\n", args[0])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "khudson:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: khudson <command> [flags]

  bus                 run the headless daemon (config, gestures, RC, registry)
  dock                run the pane-of-glass TUI on the Edge
  ctl <cmd> [arg]     talk to the bus: reload | layout <name> | theme <day|night> |
                      caffeinate <on|off|toggle> | status
  hud-launcher        supervise the HUD kitty; launches only while the display is connected
  claude focus <sid>  focus the main-kitty window hosting a claude session
  claude resume <sid> [cwd]
                      relaunch a spool-backed session in a new main-kitty tab
                      (focuses instead when it is still running)
  ax unminimize <title> [--app <name>]
                      unminimize one window: press its Dock item (direct AX;
                      --app enables the in-app window fallback)
  ax status           print whether khudson holds the Accessibility grant
  config vet <file>   validate a config against the embedded schema
  hook -dir <spool> <event>
                      claude-code hook handler (payload on stdin); events:
                      prompt | start | stop | stopfail | notify | end
`)
}

// logStart re-arms stdlib log timestamps and stamps one line at service
// start. cuelang.org/go/internal/core/adt zeroes the log flags process-wide
// in an init() (for its own bare debug output), which left the service logs
// undatable -- every long-running entrypoint calls this after that init has
// run, before its service loop.
func logStart(service string) {
	log.SetFlags(log.LstdFlags)
	version := "unknown"
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		version = bi.Main.Version
	}
	log.Printf("khudson %s: start pid=%d version=%s", service, os.Getpid(), version)
}

// histFlushEvery is the history-snapshot cadence. The periodic flush is the
// load-bearing persistence mechanism: launchctl kickstart -k may SIGKILL,
// so the shutdown flush is only best-effort.
const histFlushEvery = 5 * time.Minute

func cmdBus(args []string) error {
	fs := flag.NewFlagSet("bus", flag.ExitOnError)
	configPath := fs.String("config", "", "config file (default: embedded example)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := paths.Ensure()
	if err != nil {
		return err
	}
	logStart("bus")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// only the module.Persistent constructors (cpumem, disk) are process-
	// wide singletons -- registry.All() builds other modules fresh per call
	// -- so this call and bus.Run's own registry.All() resolve the same
	// ring-holding instances (pinned by the registry package's
	// TestPersistentModulesAreSingletons)
	var pers []module.Persistent
	for _, m := range registry.All() {
		if pm, ok := m.(module.Persistent); ok {
			pers = append(pers, pm)
		}
	}
	restoreHist(p.HistSnap(), pers)
	ready := make(chan struct{})
	flushDone := make(chan struct{})
	go func() {
		defer close(flushDone)
		// ready wins when both are up: a SIGTERM racing a successful socket
		// claim must not skip the shutdown flush of an owning bus
		select {
		case <-ready:
		default:
			select {
			case <-ctx.Done():
				return // bus never owned the socket: no flush, ever
			case <-ready:
			}
		}
		tick := time.NewTicker(histFlushEvery)
		defer tick.Stop()
		histsnap.FlushLoop(ctx, p.HistSnap(), pers, tick.C)
	}()

	err = bus.Run(ctx, bus.Options{ConfigPath: *configPath, Paths: p,
		Ready: func() { close(ready) }})
	stop() // bus.Run can fail with ctx still live; the shutdown flush needs the cancel
	<-flushDone
	return err
}

// restoreHist loads the history snapshot into the Persistent modules:
// series older than their window are dropped, shorter restart gaps padded
// with filler samples (histsnap.Prepare). Corrupt or missing snapshots
// start fresh -- loudly, never fatally.
func restoreHist(path string, pers []module.Persistent) {
	series, err := histsnap.Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Printf("khudson bus: no hist snapshot at %s; starting fresh", path)
		} else {
			log.Printf("khudson bus: hist snapshot %s unreadable, starting fresh: %v", path, err)
		}
		return
	}
	now := time.Now()
	kept := histsnap.Prepare(series, now)
	if len(kept) == 0 {
		log.Printf("khudson bus: hist snapshot %s: all %d series older than their window; starting fresh", path, len(series))
		return
	}
	for _, pm := range pers {
		pm.HistRestore(kept)
	}
	log.Printf("khudson bus: restored %d/%d history series (age %s) from %s",
		len(kept), len(series), histsnap.Age(series, now).Round(time.Second), path)
}

func cmdDock(args []string) error {
	fs := flag.NewFlagSet("dock", flag.ExitOnError)
	configPath := fs.String("config", "", "config file (default: embedded example)")
	socket := fs.String("socket", "", "bus socket (default: state dir khudson.sock)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *socket == "" {
		p, err := paths.Resolve()
		if err != nil {
			return err
		}
		*socket = p.BusSocket()
	}
	logStart("dock")
	return dock.Run(dock.Options{ConfigPath: *configPath, BusSocket: *socket})
}

func cmdCtl(args []string) error {
	fs := flag.NewFlagSet("ctl", flag.ExitOnError)
	socket := fs.String("socket", "", "bus socket (default: state dir khudson.sock)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("ctl: need a command: reload | layout <name> | theme <day|night> | caffeinate <on|off|toggle> | status")
	}
	cmd, arg := rest[0], ""
	switch cmd {
	case "reload", "status":
	case "layout":
		if len(rest) < 2 {
			return fmt.Errorf("ctl layout: need a layout name")
		}
		arg = rest[1]
	case "theme":
		if len(rest) < 2 {
			return fmt.Errorf("ctl theme: need day or night")
		}
		arg = rest[1]
	case "caffeinate":
		if len(rest) < 2 || (rest[1] != "on" && rest[1] != "off" && rest[1] != "toggle") {
			return fmt.Errorf("ctl caffeinate: need on, off, or toggle")
		}
		arg = rest[1]
	default:
		return fmt.Errorf("ctl: unknown command %q", cmd)
	}

	if *socket == "" {
		p, err := paths.Resolve()
		if err != nil {
			return err
		}
		*socket = p.BusSocket()
	}
	conn, err := net.DialTimeout("unix", *socket, 2*time.Second)
	if err != nil {
		return fmt.Errorf("bus not reachable at %s: %w", *socket, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}

	enc := json.NewEncoder(conn)
	if err := enc.Encode(proto.Msg{Type: proto.TypeHello, Role: proto.RoleCtl}); err != nil {
		return err
	}
	if err := enc.Encode(proto.Msg{Type: proto.TypeCtl, Cmd: cmd, Arg: arg}); err != nil {
		return err
	}
	var resp proto.Msg
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	if len(resp.Data) > 0 {
		var pretty map[string]any
		if err := json.Unmarshal(resp.Data, &pretty); err == nil {
			out, _ := json.MarshalIndent(pretty, "", "  ")
			fmt.Println(string(out))
		} else {
			fmt.Println(string(resp.Data))
		}
		return nil
	}
	fmt.Println("ok")
	return nil
}

func cmdHudLauncher(args []string) error {
	fs := flag.NewFlagSet("hud-launcher", flag.ExitOnError)
	kittyBin := fs.String("kitty", "kitty", "kitty executable for the HUD instance")
	kittyConfig := fs.String("kitty-config", "", "kitty config for the HUD instance (default: kitty defaults)")
	configPath := fs.String("config", "", "dock config file (default: embedded example)")
	display := fs.String("display", "XENEON EDGE", "display name the HUD pins to")
	socket := fs.String("socket", "", "HUD kitty RC socket (default: state dir kitty-panel.sock)")
	poll := fs.Duration("poll", 15*time.Second, "display presence poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := paths.Ensure()
	if err != nil {
		return err
	}
	if *socket == "" {
		*socket = p.HudKittySocket()
	}
	khudsonBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve khudson binary: %w", err)
	}
	logStart("hud-launcher")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return hudlaunch.Run(ctx, hudlaunch.Options{
		KittyBin:    *kittyBin,
		KittyConfig: *kittyConfig,
		KhudsonBin:  khudsonBin,
		DockConfig:  *configPath,
		DisplayName: *display,
		Socket:      *socket,
		Poll:        *poll,
	})
}

// cmdClaude runs the claude session verbs. The panel's row Acts exec
// `khudson claude focus <sid>` via the bus's handleRowAct (which vets the
// argv against the published acts and surfaces a nonzero exit, nothing
// more), so the wrapper logs its own outcomes to
// <state root>/log/claude-verbs.log as well as stderr.
func cmdClaude(args []string) error {
	fs := flag.NewFlagSet("claude", flag.ExitOnError)
	socket := fs.String("socket", "", "main kitty RC socket (default: state dir main-kitty.sock)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("claude: need a verb and a session id: focus <sid> | resume <sid> [cwd]")
	}
	verb, sid := rest[0], rest[1]
	v, err := bus.NewClaudeVerbs()
	if err != nil {
		return err
	}
	if *socket != "" {
		v.Socket = *socket
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch verb {
	case "focus":
		return v.Focus(ctx, sid)
	case "resume":
		cwd := ""
		if len(rest) > 2 {
			cwd = rest[2]
		}
		return v.Resume(ctx, sid, cwd)
	default:
		return fmt.Errorf("claude: unknown verb %q (focus | resume)", verb)
	}
}

// cmdAx runs the accessibility verbs behind dock-mirror's minimized rows,
// whose Act argv is `khudson ax unminimize <title> [--app <name>]`, exec'd
// bus-side by handleRowAct. FLAG GOTCHA: that argv puts --app AFTER the
// positional title and stdlib flag stops at the first non-flag, so the
// title is taken from rest[0] before Parse -- never Parse the whole tail.
func cmdAx(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("ax: need a verb: unminimize <title> [--app <name>] | status")
	}
	switch args[0] {
	case "status":
		if ax.Trusted() {
			fmt.Println("trusted: yes")
		} else {
			fmt.Println("trusted: no")
		}
		return nil
	case "unminimize":
		rest := args[1:]
		if len(rest) == 0 {
			return fmt.Errorf("ax unminimize: need a window title")
		}
		title := rest[0]
		fs := flag.NewFlagSet("ax unminimize", flag.ExitOnError)
		app := fs.String("app", "", "owning app for the in-app window fallback")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		if !ax.Trusted() {
			return fmt.Errorf("ax unminimize: Accessibility not granted -- grant the installed khudson binary in System Settings > Privacy & Security > Accessibility, then restart the bus")
		}
		err := ax.PressMinimizedItem(title)
		if errors.Is(err, ax.ErrNotFound) && *app != "" {
			// tap-vs-sweep race: the dock item vanished between render and
			// tap; fall back to the app's own window attributes
			return ax.UnminimizeWindow(*app, title)
		}
		return err
	default:
		return fmt.Errorf("ax: unknown verb %q (unminimize | status)", args[0])
	}
}

// cmdHook is the claude-code hook handler: one static-binary fork per
// fire instead of the bash+jq scripts it replaced (~65-70ms measured for
// their 4-9 child forks). Payload on stdin; errors surface on stderr but
// the hook contract stays fire-and-forget.
func cmdHook(args []string) error {
	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	dir := fs.String("dir", "", "spool directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || fs.NArg() != 1 {
		return fmt.Errorf("usage: khudson hook -dir <spool> <prompt|start|stop|stopfail|notify|end>")
	}
	return hookspool.Run(fs.Arg(0), *dir, os.Stdin, os.Getenv, time.Now())
}

func cmdConfig(args []string) error {
	if len(args) < 1 || args[0] != "vet" {
		return fmt.Errorf("config: only 'vet <file>' is supported")
	}
	if len(args) < 2 {
		return fmt.Errorf("config vet: need a file")
	}
	path := args[1]
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := config.Load(path, src); err != nil {
		return err
	}
	fmt.Println(path, "ok")
	return nil
}
