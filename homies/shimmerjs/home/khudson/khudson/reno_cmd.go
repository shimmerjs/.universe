// khudson reno: the demo presenter/recorder verb family. Lifecycle verbs
// (load/start/stop/status/next/prev/goto) ride ctl to the bus module;
// action verbs (focus-session/launch-session/open-url/focus-app) ARE the
// exec'd step effects -- the bus effect executor runs these same argvs,
// so a demo stays drivable from a bare shell with no dock attached.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/ax"
	"github.com/shimmerjs/khudson/khudson/internal/bus"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// renoVerbDeadline bounds every action verb end to end (the referenced
// kitten calls carry their own 10s sub-deadlines): nothing this binary
// runs as a step effect may outlive it.
const renoVerbDeadline = 30 * time.Second

func cmdReno(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("reno: need a verb: vet | load | start | stop | status | next | prev | goto | record | config | notes | obs | trim | stitch | mute | dub | focus-session | launch-session | open-url | focus-app | chrome-tab | hide-app | show-app")
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "vet":
		if len(rest) != 1 {
			return fmt.Errorf("reno vet: need a script path")
		}
		s, err := renocore.Load(rest[0])
		if err != nil {
			return err
		}
		fmt.Printf("%s ok: demo %s, %d steps\n", rest[0], s.Name, len(s.Steps))
		return nil

	case "load":
		// positional path + optional --replace, order-tolerant; the path
		// crosses to the bus process, so it must be absolute
		var path string
		replace := false
		for _, a := range rest {
			if a == "--replace" {
				replace = true
				continue
			}
			if path != "" {
				return fmt.Errorf("reno load: one script path")
			}
			path = a
		}
		if path == "" {
			return fmt.Errorf("reno load: need a script path")
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		// pre-vet locally for fast, rich errors; the bus re-vets as the
		// authority when it stages
		if _, err := renocore.Load(abs); err != nil {
			return err
		}
		ctlArgs := []string{"load", abs}
		if replace {
			ctlArgs = append(ctlArgs, "--replace")
		}
		return renoCtlCall(ctlArgs)

	case "start", "stop", "status", "next", "prev":
		if len(rest) != 0 {
			return fmt.Errorf("reno %s: takes no arguments", verb)
		}
		return renoCtlCall([]string{verb})

	case "goto":
		if len(rest) != 1 {
			return fmt.Errorf("reno goto: need a step id")
		}
		return renoCtlCall([]string{"goto", rest[0]})

	case "notes":
		// scriptless speaker notes: an absolute path crosses to the bus
		if len(rest) != 1 {
			return fmt.Errorf("reno notes: need a markdown path or --clear")
		}
		arg := rest[0]
		if arg != "--clear" {
			abs, err := filepath.Abs(arg)
			if err != nil {
				return err
			}
			arg = abs
		}
		return renoCtlCall([]string{"notes", arg})

	case "config":
		// the ad hoc recording config -- same core the script headers and
		// recorder-view menus drive; applies at the next record arm
		fs := flag.NewFlagSet("reno config", flag.ExitOnError)
		var screens stringsFlag
		fs.Var(&screens, "screen", "display selector, optionally :x,y,WxH (repeatable)")
		audio := fs.String("audio", "", "audio input device query, or 'none' to mute all")
		webcam := fs.String("webcam", "", "webcam preset: off|br|tr|bl|tl|big")
		clear := fs.Bool("clear", false, "reset to 'obs as-is'")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *clear {
			if len(screens) > 0 || *audio != "" || *webcam != "" {
				return fmt.Errorf("reno config: --clear takes no other flags")
			}
			return renoCtlCall([]string{"record-config", "--clear"})
		}
		var cfg renocore.RecordConfig
		for _, s := range screens {
			sel, err := renocore.ParseScreenSel(s)
			if err != nil {
				return err
			}
			cfg.Screens = append(cfg.Screens, sel)
		}
		cfg.Audio = *audio
		if *webcam != "" {
			if err := renocore.CheckWebcam(*webcam); err != nil {
				return err
			}
			cfg.Webcam = *webcam
		}
		if cfg.Empty() {
			return fmt.Errorf("reno config: nothing set (want --screen/--audio/--webcam, or --clear)")
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		return renoCtlCall([]string{"record-config", string(raw)})

	case "focus-session", "launch-session", "open-url", "open-clip", "focus-app",
		"chrome-tab", "hide-app", "show-app":
		return renoAction(verb, rest)

	case "trim", "stitch", "mute", "dub":
		return renoPost(verb, rest)

	case "record":
		// start recording: arms over ctl (ad hoc run when nothing is
		// live; the module journals intent write-ahead), then drives OBS
		// -- launching it first when the socket is down (ObsBoot)
		fs := flag.NewFlagSet("reno record", flag.ExitOnError)
		host := fs.String("obs-host", bus.DefaultObsHost, "obs-websocket host:port")
		run := fs.String("run", "", "run id (correlation)")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		v, err := bus.NewRenoVerbs()
		if err != nil {
			return err
		}
		defer renoWatchdogFor(bus.EnsureDeadline + 30*time.Second)()
		return v.Record(ensureDial(*host), renoCtlData, bus.RenoCorr{Run: *run, Step: -1, Act: -1})

	case "obs":
		// transport ops: stop | pause | resume | scene <name> stay
		// dial-only (stopping a recording must never boot OBS); ensure
		// and probe own the launch path
		if len(rest) == 0 {
			return fmt.Errorf("reno obs: need an op: ensure | arm | release | quit | probe | apply | window <show|hide|toggle> | vcam <status|stop> | cam <fx> | stop | pause | resume | scene <name>")
		}
		op := rest[0]
		arg := ""
		flagsAt := 1
		var camArgs []string
		switch op {
		case "scene", "window", "vcam":
			if len(rest) < 2 {
				return fmt.Errorf("reno obs %s: need an argument", op)
			}
			arg, flagsAt = rest[1], 2
		case "cam":
			// variadic positional FX words before the correlation flags
			// (`cam move big 600ms bounce --run r1`)
			for _, a := range rest[1:] {
				if strings.HasPrefix(a, "-") {
					break
				}
				camArgs = append(camArgs, a)
			}
			flagsAt = 1 + len(camArgs)
		}
		fs := flag.NewFlagSet("reno obs", flag.ExitOnError)
		host := fs.String("obs-host", bus.DefaultObsHost, "obs-websocket host:port")
		run := fs.String("run", "", "run id (correlation)")
		step := fs.Int("step", -1, "step ordinal (correlation)")
		act := fs.Int("act", -1, "action index (correlation)")
		sustain := fs.Bool("sustain", false, "drive a sustained cam effect until stdin closes (jitter, lava)")
		if err := fs.Parse(rest[flagsAt:]); err != nil {
			return err
		}
		corr := bus.RenoCorr{Run: *run, Step: *step, Act: *act}
		switch op {
		case "ensure":
			defer renoWatchdogFor(bus.EnsureDeadline + 30*time.Second)()
			obs, err := ensureDial(*host)()
			if err != nil {
				return err
			}
			defer obs.Close()
			fmt.Printf("obs %s up at %s (SetRecordDirectory=%v)\n", obs.Version(), *host, obs.CanSetRecordDirectory())
			return nil
		case "arm":
			v, err := bus.NewRenoVerbs()
			if err != nil {
				return err
			}
			// the launching dial: booting OBS IS the job of this verb
			defer renoWatchdogFor(bus.EnsureDeadline + 30*time.Second)()
			return v.ArmRig(ensureDial(*host), renoCtlData, corr)
		case "release":
			v, err := bus.NewRenoVerbs()
			if err != nil {
				return err
			}
			// dial-only: releasing a cold OBS is a no-op, never a launch
			defer renoWatchdog()()
			return v.Release(func() (bus.ObsTransport, error) {
				return bus.DialOBS(*host, bus.ObsPassword())
			}, renoCtlData, corr)
		case "quit":
			v, err := bus.NewRenoVerbs()
			if err != nil {
				return err
			}
			// dial-only, like release: quitting an OBS that is not running
			// must never boot one first. The budget covers the grace the
			// verb spends watching for the app to actually go.
			defer renoWatchdogFor(renoVerbDeadline + bus.ObsQuitGrace)()
			return v.Quit(func() (bus.ObsTransport, error) {
				return bus.DialOBS(*host, bus.ObsPassword())
			}, renoCtlData, corr)
		case "window":
			v, err := bus.NewRenoVerbs()
			if err != nil {
				return err
			}
			defer renoWatchdog()()
			return v.ObsWindow(arg, renoCtlData, corr)
		case "vcam":
			v, err := bus.NewRenoVerbs()
			if err != nil {
				return err
			}
			// dial-only: a cold OBS cannot be running its own virtual camera
			defer renoWatchdog()()
			return v.VirtualCam(arg, func() (bus.ObsTransport, error) {
				return bus.DialOBS(*host, bus.ObsPassword())
			}, renoCtlData, corr)
		case "probe":
			v, err := bus.NewRenoVerbs()
			if err != nil {
				return err
			}
			defer renoWatchdogFor(bus.EnsureDeadline + 30*time.Second)()
			return v.Probe(ensureDial(*host), renoCtlData, corr)
		}
		v, err := bus.NewRenoVerbs()
		if err != nil {
			return err
		}
		dial := func() (bus.ObsTransport, error) { return bus.DialOBS(*host, bus.ObsPassword()) }
		if op == "cam" {
			fx, err := renocore.ParseCamFX(camArgs)
			if err != nil {
				return err
			}
			if *sustain {
				// NO watchdog: this one is meant to outlive every other verb,
				// and its own lifetime cap is what bounds it. The supervisor
				// stops it by closing stdin, so an orphaned driver dies with
				// the pipe rather than shaking a camera nobody is watching.
				return v.SustainFX(fx, dial, renoCtlData, corr, sustainStop())
			}
			// an eased move PLAYS inside the batch, so the bound is the FX
			// budget on top of the ordinary verb deadline
			defer renoWatchdogFor(renocore.FXMaxDur + renoVerbDeadline)()
			return v.CamFX(fx, dial, renoCtlData, corr)
		}
		defer renoWatchdog()()
		if op == "apply" {
			return v.ApplyConfig(dial, renoCtlData, corr)
		}
		return v.ObsOp(op, arg, dial, renoCtlData, corr)
	}
	return fmt.Errorf("reno: unknown verb %q", verb)
}

// sustainStop closes when the driver should put its effect back and go:
// stdin reaching EOF, or a signal.
//
// Stdin is the load-bearing one. The bus module supervising a driver stops
// it by closing the pipe, which is a stop that survives the supervisor
// itself dying -- a SIGKILLed bus (kickstart -k) reaps nothing and sends
// nothing, but its end of every pipe closes anyway. Signals are for the
// hand-run case, where the driver is a foreground process and ctrl-C has
// to leave the camera where it found it.
func sustainStop() <-chan struct{} {
	stop := make(chan struct{})
	var once sync.Once
	done := func() { once.Do(func() { close(stop) }) }

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		done()
	}()
	go func() {
		// the read never returns data anybody wants: EOF is the message
		_, _ = io.Copy(io.Discard, os.Stdin)
		done()
	}()
	return stop
}

// ensureDial is the launching dial: seed the websocket config and boot
// OBS when the socket is down (record/ensure/probe only).
func ensureDial(host string) func() (bus.ObsTransport, error) {
	return func() (bus.ObsTransport, error) {
		boot, err := bus.NewObsBoot(host)
		if err != nil {
			return nil, err
		}
		return boot.Ensure(bus.EnsureDeadline)
	}
}

// stringsFlag is a repeatable string flag (--screen ... --screen ...).
type stringsFlag []string

func (s *stringsFlag) String() string { return strings.Join(*s, ",") }

func (s *stringsFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// renoWatchdog hard-bounds a verb process: goobs and cgo AX calls take
// no context, and the effect executor's shutdown story leans on every
// verb self-terminating -- so outliving the deadline exits loudly
// instead of hanging as an unkillable orphan.
func renoWatchdog() func() { return renoWatchdogFor(renoVerbDeadline) }

// renoWatchdogFor is the sized form: verbs that may cold-launch OBS
// (record, obs ensure, obs probe) run minutes, not seconds.
func renoWatchdogFor(d time.Duration) func() {
	t := time.AfterFunc(d, func() {
		fmt.Fprintln(os.Stderr, "khudson reno: verb exceeded its deadline; hard exit")
		os.Exit(124)
	})
	return func() { t.Stop() }
}

// renoCtlData sends one reno ctl subverb and returns the raw Data reply
// (the record verbs' arm/note handshake).
func renoCtlData(args []string) (json.RawMessage, error) {
	p, err := paths.Resolve()
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", p.BusSocket(), 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("bus not reachable: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, err
	}
	enc := json.NewEncoder(conn)
	if err := enc.Encode(proto.Msg{Type: proto.TypeHello, Role: proto.RoleCtl}); err != nil {
		return nil, err
	}
	if err := enc.Encode(proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: args}); err != nil {
		return nil, err
	}
	var resp proto.Msg
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	return resp.Data, nil
}

// renoCtlCall sends one reno subverb to the bus and prints the reply
// (the cmdCtl wire shape, with Args instead of Arg).
func renoCtlCall(args []string) error {
	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	sock := p.BusSocket()
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return fmt.Errorf("bus not reachable at %s: %w", sock, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	enc := json.NewEncoder(conn)
	if err := enc.Encode(proto.Msg{Type: proto.TypeHello, Role: proto.RoleCtl}); err != nil {
		return err
	}
	if err := enc.Encode(proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: args}); err != nil {
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

// renoAction runs one machine-action verb. FLAG GOTCHA: the positional
// target comes before the flags in effect argvs and stdlib flag stops at
// the first non-flag, so the target is taken from rest[0] before Parse
// (the ax pattern).
func renoAction(verb string, rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("reno %s: need a target", verb)
	}
	target := rest[0]
	fs := flag.NewFlagSet("reno "+verb, flag.ExitOnError)
	run := fs.String("run", "", "run id (correlation)")
	step := fs.Int("step", -1, "step ordinal (correlation)")
	act := fs.Int("act", -1, "action index (correlation)")
	cwd := fs.String("cwd", "", "working directory (launch-session)")
	keepFocus := fs.Bool("keep-focus", false, "pre-stage without stealing focus (launch-session)")
	if err := fs.Parse(rest[1:]); err != nil {
		return err
	}
	corr := bus.RenoCorr{Run: *run, Step: *step, Act: *act}
	defer renoWatchdog()()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// hard deadline: effect children must self-terminate -- the bus kills
	// its live effect group at clean shutdown, but a SIGKILLed bus
	// (kickstart -k) reaps nothing, so the orphan window is bounded here
	// instead of with pidfile machinery
	ctx, cancel := context.WithTimeout(ctx, renoVerbDeadline)
	defer cancel()

	v, err := bus.NewRenoVerbs()
	if err != nil {
		return err
	}
	switch verb {
	case "focus-app", "hide-app", "show-app":
		if !ax.Trusted() {
			return fmt.Errorf("reno %s: Accessibility not granted -- grant the installed khudson binary in System Settings > Privacy & Security > Accessibility, then restart the bus", verb)
		}
		var aerr error
		switch verb {
		case "focus-app":
			aerr = ax.FocusApp(target)
		case "hide-app":
			aerr = ax.SetAppHidden(target, true)
		case "show-app":
			// show = unhide + bring to front: the visibility primitive
			// that replaced spaces switching
			if aerr = ax.SetAppHidden(target, false); aerr == nil {
				aerr = ax.FocusApp(target)
			}
		}
		outcome, detail := "ok", ""
		if aerr != nil {
			outcome, detail = "error", aerr.Error()
		}
		return v.Act(corr, verb, target, outcome, detail, aerr)
	case "focus-session":
		return v.FocusSession(ctx, target, corr)
	case "launch-session":
		return v.LaunchSession(ctx, target, *cwd, *keepFocus, fs.Args(), corr)
	case "open-url":
		return v.OpenURL(ctx, target, corr)
	case "open-clip":
		return v.OpenClip(ctx, target, corr)
	case "chrome-tab":
		return v.ChromeTab(ctx, target, corr)
	}
	return fmt.Errorf("reno: unknown action verb %q", verb)
}

// renoPost runs one ffmpeg clip verb. Argv builders live in
// internal/reno (pure, unit-tested); this shell resolves defaults and
// execs. ffmpeg PATH-resolves (nixpkgs darwin ships it); a missing
// binary is a clear error, not a stack trace.
func renoPost(verb string, rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("reno %s: need a clip", verb)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("reno %s: ffmpeg not on PATH (add pkgs.ffmpeg or nix shell nixpkgs#ffmpeg)", verb)
	}
	run := func(argv []string) error {
		fmt.Fprintln(os.Stderr, "khudson reno "+verb+": "+strings.Join(argv, " "))
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return cmd.Run()
	}
	switch verb {
	case "trim":
		clip := rest[0]
		fs := flag.NewFlagSet("reno trim", flag.ExitOnError)
		start := fs.Duration("start", 0, "cut start")
		end := fs.Duration("end", 0, "cut end (required)")
		out := fs.String("o", "", "output (default <clip>-trim)")
		exact := fs.Bool("exact", false, "re-encode video for frame-accurate cuts (copy snaps to keyframes)")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		if *out == "" {
			*out = renocore.DefaultOut(clip, "trim")
		}
		argv, err := renocore.TrimArgs(clip, *start, *end, *out, *exact)
		if err != nil {
			return err
		}
		return run(argv)
	case "stitch":
		fs := flag.NewFlagSet("reno stitch", flag.ExitOnError)
		out := fs.String("o", "stitched.mkv", "output")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		list, err := renocore.ConcatList(fs.Args())
		if err != nil {
			return err
		}
		lf, err := os.CreateTemp("", "reno-stitch-*.ffconcat")
		if err != nil {
			return err
		}
		defer os.Remove(lf.Name())
		if _, err := lf.WriteString(list); err != nil {
			return err
		}
		lf.Close()
		return run(renocore.StitchArgs(lf.Name(), *out))
	case "mute":
		clip := rest[0]
		fs := flag.NewFlagSet("reno mute", flag.ExitOnError)
		out := fs.String("o", "", "output (default <clip>-mute)")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		if *out == "" {
			*out = renocore.DefaultOut(clip, "mute")
		}
		argv, err := renocore.MuteArgs(clip, *out)
		if err != nil {
			return err
		}
		return run(argv)
	case "dub":
		if len(rest) < 2 {
			return fmt.Errorf("reno dub: need a clip and an audio file")
		}
		clip, audio := rest[0], rest[1]
		fs := flag.NewFlagSet("reno dub", flag.ExitOnError)
		out := fs.String("o", "", "output (default <clip>-dub)")
		if err := fs.Parse(rest[2:]); err != nil {
			return err
		}
		if *out == "" {
			*out = renocore.DefaultOut(clip, "dub")
		}
		argv, err := renocore.DubArgs(clip, audio, *out)
		if err != nil {
			return err
		}
		return run(argv)
	}
	return fmt.Errorf("reno: unknown post verb %q", verb)
}
