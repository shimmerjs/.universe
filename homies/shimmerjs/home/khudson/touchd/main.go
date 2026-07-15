// khudson-touchd: input daemon for the khudson HUD. Owns the Corsair Xeneon Edge
// digitizer HID collection (VID 0x27C0 PID 0x0859, usage page 0x0D usage 0x04),
// asserts multi-input device mode on every open/reconnect, parses input report
// 0x0D, and broadcasts contact frames as ndjson lines on touch.sock (see Frame
// for the wire shape). The hand-debug behaviors survive as flags.
//
// Usage:
//
//	khudson-touchd -daemon             serve frames on touch.sock; reconnect on device loss
//	khudson-touchd -daemon -config f   enable modules per JSON config (default: edge+moonlander)
//	khudson-touchd -daemon -record f   also append raw reports to f
//	khudson-touchd -replay f           serve frames from a recording instead of hardware
//	khudson-touchd                     spike mode: open digitizer, switch mode, print frames
//	khudson-touchd -list               enumerate the Edge's HID collections
//	khudson-touchd -mouse              open the mouse collection instead (edgecontrol path)
//	khudson-touchd -nomode             skip the device-mode feature report
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
	mouse      bool
	noMode     bool
	daemon     bool
	config     string
	record     string
	replay     string
	socket     string
	keysSocket string
}

func main() {
	var opts options
	flag.BoolVar(&opts.list, "list", false, "enumerate Edge HID collections and exit")
	flag.BoolVar(&opts.mouse, "mouse", false, "open the mouse collection instead of the digitizer")
	flag.BoolVar(&opts.noMode, "nomode", false, "skip the device-mode feature report")
	flag.BoolVar(&opts.daemon, "daemon", false, "serve frames on the touch socket; reconnect on device loss")
	flag.StringVar(&opts.config, "config", "", "daemon module config JSON `file` (default: edge and moonlander enabled)")
	flag.StringVar(&opts.record, "record", "", "append raw report hex + timestamps to this `file`")
	flag.StringVar(&opts.replay, "replay", "", "serve frames from a recorded `file` instead of hardware")
	flag.StringVar(&opts.socket, "socket", "", "unix socket path (default ~/Library/Application Support/khudson/touch.sock)")
	flag.StringVar(&opts.keysSocket, "keys-socket", "", "Moonlander key-event socket path (default ~/Library/Application Support/khudson/keys.sock)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, opts); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options) error {
	if opts.replay != "" && (opts.daemon || opts.list || opts.mouse || opts.noMode || opts.record != "" || opts.config != "") {
		return errors.New("-replay replaces hardware; combine only with -socket")
	}
	if opts.daemon && opts.mouse {
		return errors.New("-daemon reads the digitizer collection; -mouse is a spike-mode flag")
	}

	// resolve the module set first: a config problem must exit nonzero
	// before any socket binds or HID work
	var enabled map[string]bool
	if opts.daemon {
		var err error
		if enabled, err = loadModuleConfig(opts.config); err != nil {
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
		return runDaemon(ctx, opts, enabled, rec)
	}

	return runStream(ctx, rec, opts.mouse, opts.noMode)
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
