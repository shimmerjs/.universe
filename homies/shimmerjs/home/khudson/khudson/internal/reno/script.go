// Package reno holds the demo engine's core types: the script format
// (txtar archive, testscript-style command lines in the comment section,
// markdown note files in the file sections) and the run journal. The
// package is dock- and bus-agnostic; the module and the CLI verbs build
// on it (standalone-modules doctrine: composable core, thin clients).
//
// The command vocabulary is closed by construction: every step line must
// name a registered command with valid arity, so a vetted script can only
// ever produce effect argvs the engine knows how to build. That vet is
// the trust chain behind the bus effect executor -- startArgv itself
// vets nothing.
package reno

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/tools/txtar"
)

// slugRE pins demo names and step ids to the widget-id shape: the demo
// name keys clip-workspace directories, so the pin is containment, not
// taste.
var slugRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// AdhocName is the reserved demo name for scriptless (ad hoc) runs;
// scripts may not claim it.
const AdhocName = "adhoc"

// Script is one parsed, vetted demo script.
type Script struct {
	Name   string
	Budget time.Duration // whole-talk budget; 0 = none
	// Record declares recording intent: the run records from the first
	// advance, and record/scene step commands become legal.
	Record bool
	// Scene is the optional initial OBS scene, set before recording
	// starts.
	Scene string
	// RecordCfg is the header recording config (screen/audio/webcam) --
	// the same core the ad hoc surfaces drive; empty means OBS as-is.
	RecordCfg RecordConfig
	Steps     []Step
	// Hash is the sha256 of the raw archive bytes; the run journal pins
	// it so a reload with an edited script never silently keeps a stale
	// cursor.
	Hash string
	// Path is where the script was read from ("" when parsed from bytes
	// without a file, e.g. tests).
	Path string
}

// Step is one presenter step: notes, an optional time budget, and the
// machine actions fired on entry.
type Step struct {
	ID       string
	Note     string // resolved note text; "" = none
	NoteRef  string // the note argument as written
	Budget   time.Duration
	Commands []Command
	Line     int // 1-based line of the step directive in the comment section
}

// Command is one registered step command.
type Command struct {
	Name string
	Args []string
	Line int
}

// cmdSpec vets one registered command: arity bounds (maxArgs -1 =
// unbounded) plus an optional shape check.
type cmdSpec struct {
	minArgs int
	maxArgs int
	check   func(args []string) error
}

// commands is the closed P0 registry. The names double as `khudson reno`
// CLI verbs; the module builds effect argvs from them 1:1.
var commands = map[string]cmdSpec{
	"focus-session":  {minArgs: 1, maxArgs: 1, check: checkHandle},
	"launch-session": {minArgs: 1, maxArgs: -1, check: checkLaunch},
	"focus-app":      {minArgs: 1, maxArgs: 1},
	"open-url":       {minArgs: 1, maxArgs: 1, check: checkURL},
	// recording commands are legal only under a `record` header (Parse):
	// a scene switch in a script that never records is an authoring
	// error, not a dead op
	"scene":  {minArgs: 1, maxArgs: 1},
	"record": {minArgs: 1, maxArgs: 1, check: checkRecordOp},
	// webcam FX one-shots; the placement they animate comes from the
	// webcam header, so they are record-gated twice over
	"cam": {minArgs: 1, maxArgs: 4, check: checkCamFXArgs},
	// window visibility (spaces retired, user 2026-07-22) + chrome tabs
	"chrome-tab": {minArgs: 1, maxArgs: 1},
	"hide-app":   {minArgs: 1, maxArgs: 1, check: checkBundleID},
	"show-app":   {minArgs: 1, maxArgs: 1, check: checkBundleID},
}

// checkBundleID rejects the obvious non-bundle shapes (paths, bare
// words); real resolution happens at exec via lsappinfo.
func checkBundleID(args []string) error {
	b := args[0]
	if strings.ContainsAny(b, "/ ") || !strings.Contains(b, ".") {
		return fmt.Errorf("%q does not look like a bundle id (com.vendor.App)", b)
	}
	return nil
}

// recordCommands need the header's recording intent to vet.
var recordCommands = map[string]bool{"scene": true, "record": true, "cam": true}

// reservedP2 names vet loudly as not-yet-landed instead of unknown, so a
// script written against the full design fails with the real reason.
var reservedP2 = map[string]bool{
	"wait": true, "hide": true, "show": true,
}

func checkRecordOp(args []string) error {
	if args[0] != "pause" && args[0] != "resume" {
		return fmt.Errorf("record wants pause or resume as a step command, got %q", args[0])
	}
	return nil
}

func checkCamFXArgs(args []string) error {
	_, err := ParseCamFX(args)
	return err
}

func checkHandle(args []string) error {
	if !slugRE.MatchString(args[0]) {
		return fmt.Errorf("handle %q must match %s", args[0], slugRE)
	}
	return nil
}

// checkLaunch vets `launch-session <handle> [--cwd <dir>] [--keep-focus]
// [-- <argv>...]`.
func checkLaunch(args []string) error {
	if err := checkHandle(args); err != nil {
		return err
	}
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--cwd":
			i++
			if i >= len(rest) {
				return errors.New("--cwd needs a directory")
			}
		case "--keep-focus":
		case "--":
			if i == len(rest)-1 {
				return errors.New("-- needs a command")
			}
			return nil
		default:
			return fmt.Errorf("unknown launch-session flag %q (--cwd, --keep-focus, -- <argv>)", rest[i])
		}
	}
	return nil
}

func checkURL(args []string) error {
	u := args[0]
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return fmt.Errorf("open-url wants an http(s) URL, got %q", u)
	}
	return nil
}

// MaxScriptBytes bounds a script read: scripts are prose plus command
// lines; anything bigger is not a script, and the bus reads this path
// under the module lock (arc-2 review #18).
const MaxScriptBytes = 1 << 20

// Load reads and parses the script at path. This is the one vet
// implementation: `khudson reno vet`, the CLI pre-vet, and the bus load
// all run through it. The path arrives over the ctl wire, so it is
// stat-vetted (regular file, size cap) before any read -- a FIFO would
// wedge the reader, /dev/zero would OOM it.
func Load(path string) (*Script, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > MaxScriptBytes {
		return nil, fmt.Errorf("%s is %d bytes (cap %d) -- that is not a demo script", path, info.Size(), MaxScriptBytes)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(path, src)
}

// Parse parses and vets src. Errors are collected, not first-only: a
// script author gets the whole list in one pass, each prefixed with its
// comment-section line number.
func Parse(path string, src []byte) (*Script, error) {
	arc := txtar.Parse(src)
	notes := make(map[string][]byte, len(arc.Files))
	for _, f := range arc.Files {
		notes[f.Name] = f.Data
	}

	sum := sha256.Sum256(src)
	s := &Script{Hash: hex.EncodeToString(sum[:]), Path: path}
	var errs []error
	fail := func(line int, format string, args ...any) {
		errs = append(errs, fmt.Errorf("line %d: %s", line, fmt.Sprintf(format, args...)))
	}

	var cur *Step
	stepLines := map[string]int{}
	sawBudget := false
	for i, raw := range strings.Split(string(arc.Comment), "\n") {
		line := i + 1
		text := strings.TrimSpace(raw)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		verb, args := fields[0], fields[1:]
		switch verb {
		case "demo":
			if cur != nil {
				fail(line, "demo must appear in the header, before the first step")
				continue
			}
			if s.Name != "" {
				fail(line, "duplicate demo line")
				continue
			}
			if len(args) != 1 {
				fail(line, "demo wants exactly one name")
				continue
			}
			if !slugRE.MatchString(args[0]) {
				fail(line, "demo name %q must match %s", args[0], slugRE)
				continue
			}
			if args[0] == AdhocName {
				fail(line, "demo name %q is reserved for scriptless runs", AdhocName)
				continue
			}
			s.Name = args[0]
		case "record":
			if cur == nil {
				// header form: bare recording intent
				if len(args) != 0 {
					fail(line, "header record takes no arguments (pause/resume are step commands)")
					continue
				}
				if s.Record {
					fail(line, "duplicate record header")
					continue
				}
				s.Record = true
				continue
			}
			// step form (`record pause|resume`) vets as a command below
			vetCommand(s, cur, verb, args, line, fail)
		case "scene":
			if cur == nil {
				// header form: the initial scene, set before recording starts
				if len(args) != 1 {
					fail(line, "header scene wants exactly one name")
					continue
				}
				if s.Scene != "" {
					fail(line, "duplicate scene header")
					continue
				}
				s.Scene = args[0]
				continue
			}
			vetCommand(s, cur, verb, args, line, fail)
		case "screen":
			// header-only recording config: which display(s), optional
			// region crop in display pixels
			if cur != nil {
				fail(line, "screen is a header, not a step command")
				continue
			}
			switch len(args) {
			case 1:
				s.RecordCfg.Screens = append(s.RecordCfg.Screens, ScreenSel{Display: args[0]})
			case 2:
				r, err := ParseRegion(args[1])
				if err != nil {
					fail(line, "screen: %v", err)
					continue
				}
				s.RecordCfg.Screens = append(s.RecordCfg.Screens, ScreenSel{Display: args[0], Region: r})
			default:
				fail(line, "screen wants a display selector and an optional x,y,WxH region")
			}
		case "audio":
			if cur != nil {
				fail(line, "audio is a header, not a step command")
				continue
			}
			if len(args) != 1 {
				fail(line, "audio wants one device query or %q", AudioNone)
				continue
			}
			if s.RecordCfg.Audio != "" {
				fail(line, "duplicate audio header")
				continue
			}
			s.RecordCfg.Audio = args[0]
		case "webcam":
			if cur != nil {
				fail(line, "webcam is a header, not a step command")
				continue
			}
			if len(args) != 1 {
				fail(line, "webcam wants one placement preset")
				continue
			}
			if s.RecordCfg.Webcam != "" {
				fail(line, "duplicate webcam header")
				continue
			}
			if err := CheckWebcam(args[0]); err != nil {
				fail(line, "webcam: %v", err)
				continue
			}
			s.RecordCfg.Webcam = args[0]
		case "step":
			if len(args) != 1 {
				fail(line, "step wants exactly one id")
				continue
			}
			id := args[0]
			if !slugRE.MatchString(id) {
				fail(line, "step id %q must match %s", id, slugRE)
			}
			if prev, dup := stepLines[id]; dup {
				fail(line, "step id %q already used at line %d", id, prev)
			}
			stepLines[id] = line
			s.Steps = append(s.Steps, Step{ID: id, Line: line})
			cur = &s.Steps[len(s.Steps)-1]
		case "note":
			if cur == nil {
				fail(line, "note outside a step")
				continue
			}
			if len(args) != 1 {
				fail(line, "note wants exactly one file")
				continue
			}
			if cur.NoteRef != "" {
				fail(line, "step %q already has a note (%s)", cur.ID, cur.NoteRef)
				continue
			}
			cur.NoteRef = args[0]
			body, err := resolveNote(path, notes, args[0])
			if err != nil {
				fail(line, "note %s: %v", args[0], err)
				continue
			}
			cur.Note = body
		case "budget":
			if len(args) != 1 {
				fail(line, "budget wants exactly one duration")
				continue
			}
			d, err := time.ParseDuration(args[0])
			if err != nil || d <= 0 {
				fail(line, "budget %q is not a positive duration", args[0])
				continue
			}
			if cur == nil {
				if sawBudget {
					fail(line, "duplicate demo budget")
					continue
				}
				sawBudget = true
				s.Budget = d
			} else {
				if cur.Budget != 0 {
					fail(line, "step %q already has a budget", cur.ID)
					continue
				}
				cur.Budget = d
			}
		default:
			if reservedP2[verb] {
				fail(line, "%q is reserved wait-guard vocabulary and has not landed", verb)
				continue
			}
			if cur == nil {
				fail(line, "command %q before the first step (header takes demo, budget, record, scene)", verb)
				continue
			}
			vetCommand(s, cur, verb, args, line, fail)
		}
	}
	if s.Name == "" {
		errs = append(errs, errors.New("missing demo <name> header"))
	}
	if len(s.Steps) == 0 {
		errs = append(errs, errors.New("script has no steps"))
	}
	if s.Scene != "" && !s.Record {
		errs = append(errs, errors.New("scene header without a record header"))
	}
	if !s.RecordCfg.Empty() && !s.Record {
		errs = append(errs, errors.New("screen/audio/webcam headers without a record header"))
	}
	if s.Scene != "" && !s.RecordCfg.Empty() {
		errs = append(errs, errors.New("scene header and screen/audio/webcam headers are exclusive: the config path owns the scene"))
	}
	if s.RecordCfg.Webcam == "" {
		// a cam FX animates the reno-cam item, and only the webcam header
		// puts one in the scene: without it every cam step would die at
		// exec on a source that was never staged
		for _, step := range s.Steps {
			for _, c := range step.Commands {
				if c.Name == "cam" {
					fail(c.Line, "cam: the script stages no webcam (add a `webcam <preset>` header)")
				}
			}
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return s, nil
}

// vetCommand vets one step command line against the closed registry and
// appends it to the step.
func vetCommand(s *Script, cur *Step, verb string, args []string, line int, fail func(int, string, ...any)) {
	spec, ok := commands[verb]
	if !ok {
		fail(line, "unknown command %q (registry: %s)", verb, registryNames())
		return
	}
	if recordCommands[verb] && !s.Record {
		fail(line, "%s: the script never records (add a `record` header)", verb)
		return
	}
	if len(args) < spec.minArgs || (spec.maxArgs >= 0 && len(args) > spec.maxArgs) {
		fail(line, "%s: wrong arity (got %d args)", verb, len(args))
		return
	}
	if spec.check != nil {
		if err := spec.check(args); err != nil {
			fail(line, "%s: %v", verb, err)
			return
		}
	}
	cur.Commands = append(cur.Commands, Command{Name: verb, Args: args, Line: line})
}

// resolveNote finds a note body: an archive file section by exact name,
// else a relative path under the script's directory. Absolute paths and
// any traversal above the script dir are containment errors, and
// external refs need a real script path to resolve against.
func resolveNote(scriptPath string, notes map[string][]byte, ref string) (string, error) {
	if body, ok := notes[ref]; ok {
		return string(body), nil
	}
	if filepath.IsAbs(ref) {
		return "", errors.New("absolute note paths are not allowed")
	}
	clean := filepath.Clean(ref)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("note path escapes the script directory")
	}
	if scriptPath == "" {
		return "", errors.New("not in the archive, and no script path to resolve an external ref against")
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(scriptPath), clean))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func registryNames() string {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	// deterministic error text
	for i := range names {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	return strings.Join(names, ", ")
}

// StepIndex returns the index of the step with the given id, -1 when
// absent.
func (s *Script) StepIndex(id string) int {
	for i := range s.Steps {
		if s.Steps[i].ID == id {
			return i
		}
	}
	return -1
}
