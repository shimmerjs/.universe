// magicbusd: input daemon for the khudson HUD. Owns the Corsair Xeneon Edge
// digitizer HID collection (VID 0x27C0 PID 0x0859, usage page 0x0D usage 0x04),
// asserts multi-input device mode on every open/reconnect, parses input report
// 0x0D, and broadcasts contact frames as ndjson lines on touch.sock (see Frame
// for the wire shape). The hand-debug behaviors survive as flags.
//
// Usage:
//
//	magicbusd -daemon             serve frames on touch.sock; reconnect on device loss
//	magicbusd -daemon -config f   enable modules per JSON config (default: edge+moonlander)
//	magicbusd -daemon -record f   also append raw reports to f
//	magicbusd -replay f           serve frames from a recording instead of hardware
//	magicbusd -spike              spike mode: open digitizer, switch mode, print frames
//	magicbusd logiretch-probe     one-shot MX Master 4 HID++ feasibility report (read-only)
//	magicbusd -list               enumerate the Edge's HID collections
//	magicbusd -mouse              spike on the mouse collection instead (edgecontrol path)
//	magicbusd -nomode             skip the device-mode feature report (daemon or spike)
//
// A mode flag is REQUIRED: bare argv errors instead of defaulting to spike --
// a flagless launchd invocation used to enter spike mode, die one-shot on the
// gestures-driver digitizer seize, and crash-loop silently with no sockets
// (the 33f120e incident).
//
// Requires the Input Monitoring permission (macOS prompts on first run for the
// hosting binary). On SIGINT/SIGTERM any asserted device mode is put back to
// mouse emulation so the fallback driver path works without an unplug cycle.
// Quit Touchscreen Gestures.app first if reports look doubled.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/sstallion/go-hid"
)

type options struct {
	list       bool
	spike      bool
	mouse      bool
	noMode     bool
	daemon     bool
	logiretch  bool
	config     string
	record     string
	replay     string
	socket     string
	keysSocket string
	logiSocket string
}

func main() {
	var opts options
	flag.BoolVar(&opts.list, "list", false, "enumerate Edge HID collections and exit")
	flag.BoolVar(&opts.spike, "spike", false, "spike mode: open the digitizer and print frames")
	flag.BoolVar(&opts.mouse, "mouse", false, "spike on the mouse collection instead of the digitizer")
	flag.BoolVar(&opts.noMode, "nomode", false, "skip the device-mode feature report")
	flag.BoolVar(&opts.daemon, "daemon", false, "serve frames on the touch socket; reconnect on device loss")
	flag.StringVar(&opts.config, "config", "", "daemon module config JSON `file` (default: edge and moonlander enabled)")
	flag.StringVar(&opts.record, "record", "", "append raw report hex + timestamps to this `file`")
	flag.StringVar(&opts.replay, "replay", "", "serve frames from a recorded `file` instead of hardware")
	flag.StringVar(&opts.socket, "socket", "", "unix socket path (default ~/Library/Application Support/khudson/touch.sock)")
	flag.StringVar(&opts.keysSocket, "keys-socket", "", "Moonlander key-event socket path (default ~/Library/Application Support/khudson/keys.sock)")
	flag.StringVar(&opts.logiSocket, "logi-socket", "", "logiretch (MX Master) state socket path (default ~/Library/Application Support/khudson/logiretch.sock)")
	flag.Parse()
	if arg := flag.Arg(0); arg != "" {
		if arg != "logiretch-probe" {
			fmt.Fprintf(os.Stderr, "error: unknown subcommand %q\n", arg)
			os.Exit(2)
		}
		opts.logiretch = true
		// Go flag parsing stops at the first positional, so anything after
		// the subcommand would otherwise be dropped silently -- reject it.
		if rest := flag.Args()[1:]; len(rest) > 0 {
			fmt.Fprintf(os.Stderr, "error: unexpected arguments after logiretch-probe: %v (flags go before the subcommand)\n", rest)
			os.Exit(2)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, opts); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runStreamFn is the spike-mode seam: tests swap it to pin -spike/-mouse
// routing without opening hardware.
var runStreamFn = runStream

func run(ctx context.Context, opts options) error {
	if opts.replay != "" && (opts.daemon || opts.list || opts.spike || opts.mouse || opts.noMode || opts.record != "" || opts.config != "") {
		return errors.New("-replay replaces hardware; combine only with -socket")
	}
	if opts.daemon && (opts.spike || opts.mouse) {
		return errors.New("-daemon reads the digitizer collection; -spike/-mouse are spike-mode flags")
	}
	if opts.daemon && opts.list {
		return errors.New("-list is a one-shot enumerate-and-exit; it would preempt the -daemon serve loop")
	}
	if opts.config != "" && !opts.daemon {
		return errors.New("-config is a daemon flag; use -daemon")
	}
	if opts.logiretch && (opts.daemon || opts.replay != "" || opts.list || opts.spike || opts.mouse || opts.noMode || opts.record != "") {
		return errors.New("logiretch-probe is a one-shot read-only prober; combine with no mode flags")
	}
	if opts.list && (opts.spike || opts.mouse || opts.noMode || opts.record != "") {
		return errors.New("-list is a one-shot enumerate-and-exit; spike/daemon flags do not apply")
	}
	// a mode is REQUIRED: defaulting bare argv to spike put a flagless
	// launchd invocation straight onto the hardware-seize crash loop
	if !opts.daemon && !opts.list && !opts.spike && !opts.mouse && opts.replay == "" && !opts.logiretch {
		return errors.New("no mode selected: use -daemon (service), -spike (dev frame stream; -mouse implies it), -list, -replay <file>, or the logiretch-probe subcommand")
	}

	// resolve the module set first: a config problem must exit nonzero
	// before any socket binds or HID work
	var enabled map[string]bool
	var logiCfg *logiConfig
	if opts.daemon {
		var err error
		if enabled, logiCfg, err = loadModuleConfig(opts.config); err != nil {
			return err
		}
	}

	if opts.socket == "" {
		sock, err := defaultSocket("touch.sock")
		if err != nil {
			return err
		}
		opts.socket = sock
	}
	if opts.keysSocket == "" {
		sock, err := defaultSocket("keys.sock")
		if err != nil {
			return err
		}
		opts.keysSocket = sock
	}
	if opts.logiSocket == "" {
		sock, err := defaultSocket("logiretch.sock")
		if err != nil {
			return err
		}
		opts.logiSocket = sock
	}

	if opts.replay != "" {
		reports, err := loadRecording(opts.replay)
		if err != nil {
			return err
		}
		b, err := newBroadcaster(opts.socket)
		if err != nil {
			return err
		}
		defer b.close()
		return runReplay(ctx, b, reports)
	}

	if err := hid.Init(); err != nil {
		return fmt.Errorf("hid init: %w", err)
	}
	defer hid.Exit()

	if opts.logiretch {
		return runLogiretchProbe(ctx, os.Stdout)
	}

	if opts.list {
		return enumerate()
	}

	var rec *recorder
	if opts.record != "" {
		r, err := newRecorder(opts.record)
		if err != nil {
			return err
		}
		defer r.close()
		rec = r
	}

	if opts.daemon {
		return runDaemon(ctx, opts, enabled, logiCfg, rec)
	}

	return runStreamFn(ctx, rec, opts.mouse, opts.noMode)
}

// defaultSocket resolves name under the khudson runtime dir; runtime state
// must not live in /tmp (macOS reaps idle /private/tmp entries).
func defaultSocket(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "khudson", name), nil
}
