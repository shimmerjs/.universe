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
	"net"
	"os"
	"os/signal"
	"path/filepath"
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
		return fmt.Errorf("reno: need a verb: vet | load | start | stop | status | next | prev | goto | focus-session | launch-session | open-url | focus-app")
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

	case "focus-session", "launch-session", "open-url", "focus-app":
		return renoAction(verb, rest)

	case "record":
		// start recording: arms over ctl (ad hoc run when nothing is
		// live; the module journals intent write-ahead), then drives OBS
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
		dial := func() (bus.ObsTransport, error) { return bus.DialOBS(*host, bus.ObsPassword()) }
		return v.Record(dial, renoCtlData, bus.RenoCorr{Run: *run, Step: -1, Act: -1})

	case "obs":
		// transport ops: stop | pause | resume | scene <name>
		if len(rest) == 0 {
			return fmt.Errorf("reno obs: need an op: stop | pause | resume | scene <name>")
		}
		op := rest[0]
		arg := ""
		flagsAt := 1
		if op == "scene" {
			if len(rest) < 2 {
				return fmt.Errorf("reno obs scene: need a scene name")
			}
			arg, flagsAt = rest[1], 2
		}
		fs := flag.NewFlagSet("reno obs", flag.ExitOnError)
		host := fs.String("obs-host", bus.DefaultObsHost, "obs-websocket host:port")
		run := fs.String("run", "", "run id (correlation)")
		step := fs.Int("step", -1, "step ordinal (correlation)")
		act := fs.Int("act", -1, "action index (correlation)")
		if err := fs.Parse(rest[flagsAt:]); err != nil {
			return err
		}
		v, err := bus.NewRenoVerbs()
		if err != nil {
			return err
		}
		dial := func() (bus.ObsTransport, error) { return bus.DialOBS(*host, bus.ObsPassword()) }
		return v.ObsOp(op, arg, dial, renoCtlData, bus.RenoCorr{Run: *run, Step: *step, Act: *act})
	}
	return fmt.Errorf("reno: unknown verb %q", verb)
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// hard deadline: effect children must self-terminate -- the bus kills
	// its live effect group at clean shutdown, but a SIGKILLed bus
	// (kickstart -k) reaps nothing, so the orphan window is bounded here
	// instead of with pidfile machinery
	ctx, cancel := context.WithTimeout(ctx, renoVerbDeadline)
	defer cancel()

	if verb == "focus-app" {
		if !ax.Trusted() {
			return fmt.Errorf("reno focus-app: Accessibility not granted -- grant the installed khudson binary in System Settings > Privacy & Security > Accessibility, then restart the bus")
		}
		v, err := bus.NewRenoVerbs()
		if err != nil {
			return err
		}
		aerr := ax.FocusApp(target)
		outcome, detail := "ok", ""
		if aerr != nil {
			outcome, detail = "error", aerr.Error()
		}
		return v.Act(corr, "focus-app", target, outcome, detail, aerr)
	}

	v, err := bus.NewRenoVerbs()
	if err != nil {
		return err
	}
	switch verb {
	case "focus-session":
		return v.FocusSession(ctx, target, corr)
	case "launch-session":
		return v.LaunchSession(ctx, target, *cwd, *keepFocus, fs.Args(), corr)
	case "open-url":
		return v.OpenURL(ctx, target, corr)
	}
	return fmt.Errorf("reno: unknown action verb %q", verb)
}
