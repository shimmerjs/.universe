package main

import (
	"bufio"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"strings"
	"time"

	"github.com/shimmerjs/krib/classify"
	"github.com/shimmerjs/krib/envelope"
	"github.com/shimmerjs/krib/sheets"
)

// runExec resolves one accepted entry id against the session cache (--data;
// the palette scrapes once, nothing here re-runs it) and executes it through
// the sheet's exec descriptor.
func runExec(args []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	data := fs.String("data", "", "envelope file (the session cache; default stdin)")
	from := fs.String("from", "auto", "input format: auto, envelope, kitty-jsonl")
	sheetFlag := fs.String("sheet", "", "sheet config: embedded name or JSON file path (default kitty)")
	window := fs.String("window", "", "kitty window id to target (the palette overlay's parent)")
	yes := fs.Bool("yes", false, "run a confirm-flagged entry without prompting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: krib exec [--data file] [--sheet name|path] [--window id] [--yes] <id>")
	}
	return execID(fs.Arg(0), *data, *from, *sheetFlag, *window, *yes)
}

func execID(id, data, from, sheetFlag, window string, yes bool) error {
	env, err := decodeData(data, from)
	if err != nil {
		return err
	}
	sheet, err := sheets.Load(sheetFlag)
	if err != nil {
		return err
	}
	en, ok := entryByID(env, id)
	if !ok {
		return fmt.Errorf("no entry %q in the data", id)
	}
	return executeEntry(env, sheet, en, window, yes)
}

// entryByID resolves an accepted id against the already-decoded envelope.
func entryByID(env *envelope.Envelope, id string) (envelope.Entry, bool) {
	for _, en := range env.Entries {
		if en.ID(env.Kind) == id {
			return en, true
		}
	}
	return envelope.Entry{}, false
}

// executeEntry gates, records usage, and runs one entry through the sheet's
// exec descriptor (EntryRule.Exec overrides sheet.Exec). window targets the
// palette overlay's PARENT so window-relative actions do not hit the overlay.
func executeEntry(env *envelope.Envelope, sheet classify.Sheet, en envelope.Entry, window string, yes bool) error {
	id := en.ID(env.Kind)
	spec := sheet.Exec
	needsConfirm := false
	if rule := sheet.Rule(en.Cmd); rule != nil {
		needsConfirm = rule.Confirm
		if rule.Exec != nil {
			spec = rule.Exec
		}
	}
	if spec == nil {
		return fmt.Errorf("sheet %q declares no exec behavior for %q", sheet.Name, id)
	}
	if spec.Run == "none" {
		return fmt.Errorf("entry %q is not runnable (exec none)", id)
	}
	if needsConfirm && !yes {
		ok, err := confirmEntry(displayFor(env, en))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "krib: not confirmed, not running", id)
			return nil
		}
	}
	recordUse(env, id)
	switch spec.Run {
	case "copy":
		return copyOSC52(clipboardW, en.Cmd)
	case "run":
		argv, err := execArgv(spec.Argv, en.Cmd, window)
		if err != nil {
			return err
		}
		return runArgv(argv)
	default:
		return fmt.Errorf("unknown exec run %q", spec.Run)
	}
}

// spawn/prompt/clipboard seams, swapped out by tests.
var (
	runArgv = func(argv []string) error {
		cmd := osexec.Command(argv[0], argv[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	confirmEntry           = confirmOnTTY
	clipboardW   io.Writer = os.Stdout
)

func displayFor(env *envelope.Envelope, en envelope.Entry) string {
	if env.Kind == envelope.KindCards {
		return en.Term
	}
	return en.Cmd
}

// execArgv renders an exec argv template: "{cmd}" becomes the entry's raw
// Cmd string as one argument (argv exec, no shell); an element containing
// "{window}" substitutes the target window id, and is dropped entirely when
// no target was given.
func execArgv(tmpl []string, cmd, window string) ([]string, error) {
	if strings.TrimSpace(cmd) == "" {
		return nil, fmt.Errorf("entry has no command")
	}
	out := make([]string, 0, len(tmpl))
	for _, a := range tmpl {
		switch {
		case a == "{cmd}":
			out = append(out, cmd)
		case strings.Contains(a, "{window}"):
			if window == "" {
				continue
			}
			out = append(out, strings.ReplaceAll(a, "{window}", window))
		default:
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("exec argv template rendered empty")
	}
	return out, nil
}

// confirm reads one line from r and accepts y/yes (case-insensitive). A
// confirm-flagged entry never fires on the first accept: this second step
// (or an explicit --yes) is required.
func confirm(r io.Reader, w io.Writer, label string) bool {
	fmt.Fprintf(w, "krib: run %q? [y/N] ", label)
	line, _ := bufio.NewReader(r).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func confirmOnTTY(label string) (bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, fmt.Errorf("entry requires confirmation and there is no tty; re-run with --yes")
	}
	defer tty.Close()
	return confirm(tty, tty, label), nil
}

// recordUse bumps the accept count in the statefile; usage tracking never
// blocks execution. The envelope is observed first so a fresh statefile gets
// real value-hashes -- RecordUse alone would create a hashless entry that the
// next list/print misreads as a value change.
func recordUse(env *envelope.Envelope, id string) {
	st, path := loadState(env)
	if path == "" {
		return
	}
	now := time.Now()
	st.Observe(env, now)
	st.RecordUse(id, now)
	if err := st.Save(path); err != nil {
		fmt.Fprintln(os.Stderr, "krib: warning: statefile:", err)
	}
}

// copyOSC52 puts text on the clipboard via OSC 52 (kitty honors it per
// clipboard_control).
func copyOSC52(w io.Writer, text string) error {
	_, err := fmt.Fprintf(w, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
	return err
}
