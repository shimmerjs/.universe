// Reno action verbs: the `khudson reno focus-session|launch-session|
// open-url` wrappers that the shared effect executor (and a bare shell)
// runs. Same discipline as the claude verbs -- a FRESH `kitten @ ls`
// before acting, resolution at exec time, socket-file auth -- plus the
// deltas the demo engine needs: resolution by the reno_handle user var
// with duplicate handles rejected loudly, a post-action read confirming
// the act landed, and correlated structured events (run id + step +
// action index) journaled to <state root>/reno/log/events.jsonl. Journal
// write failures FAIL THE VERB (nonzero exit -> ActFail warn cell):
// wait guards and post-mortems read this journal, so a silent miss is
// worse than a dead step.
package renocontrol

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
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/rc"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
	"github.com/shimmerjs/khudson/khudson/internal/subprocess"
)

// renoHandleVar is the kitty user var planted at launch --var carrying a
// demo window's handle; it survives shell title rewrites.
const renoHandleVar = "reno_handle"

// RenoCorr correlates one verb invocation with its script position.
// Step/Act are -1 when the verb runs by hand.
type RenoCorr struct {
	Run  string
	Step int
	Act  int
}

// RenoVerbs is the wrapper's wiring: main-kitty socket, events journal,
// exec seams.
type RenoVerbs struct {
	Socket     string
	EventsPath string
	Run        kittenRunner
	// openStart execs `open <url>`; seam for tests.
	openStart func(ctx context.Context, argv []string) error
	now       func() time.Time
}

// NewRenoVerbs wires the defaults from the state root.
func NewRenoVerbs() (*RenoVerbs, error) {
	p, err := paths.Resolve()
	if err != nil {
		return nil, err
	}
	return &RenoVerbs{
		Socket:     p.MainKittySocket(),
		EventsPath: filepath.Join(p.Dir, "reno", "log", "events.jsonl"),
		Run:        runKitten,
		openStart: func(ctx context.Context, argv []string) error {
			return exec.CommandContext(ctx, argv[0], argv[1:]...).Run()
		},
		now: time.Now,
	}, nil
}

// renoEvent is one journaled action outcome.
type renoEvent struct {
	TS      string `json:"ts"`
	Run     string `json:"run,omitempty"`
	Step    int    `json:"step"`
	Act     int    `json:"act"`
	Verb    string `json:"verb"`
	Target  string `json:"target"`
	Outcome string `json:"outcome"` // ok | miss | ambiguous | unconfirmed | error
	Detail  string `json:"detail,omitempty"`
}

// event journals one outcome (append-only jsonl) and echoes it to
// stderr. The returned error is the journal write failure -- callers
// fold it into their exit status instead of swallowing it.
func (v *RenoVerbs) event(corr RenoCorr, verb, target, outcome, detail string) error {
	e := renoEvent{
		TS: v.now().Format(time.RFC3339), Run: corr.Run, Step: corr.Step, Act: corr.Act,
		Verb: verb, Target: target, Outcome: outcome, Detail: detail,
	}
	fmt.Fprintf(os.Stderr, "khudson reno %s %s: %s %s\n", verb, target, outcome, detail)
	if v.EventsPath == "" {
		return nil
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(v.EventsPath), 0o700); err != nil {
		return fmt.Errorf("reno events journal: %w", err)
	}
	f, err := diagnostic.OpenFile(v.EventsPath, 4<<20)
	if err != nil {
		return fmt.Errorf("reno events journal: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("reno events journal: %w", err)
	}
	return nil
}

// Act journals the outcome and merges journal failure into the verb
// result: a verb only exits 0 when the action landed AND the journal
// recorded it.
func (v *RenoVerbs) Act(corr RenoCorr, verb, target, outcome, detail string, actErr error) error {
	jerr := v.event(corr, verb, target, outcome, detail)
	return errors.Join(actErr, jerr)
}

func (v *RenoVerbs) freshLS(ctx context.Context) ([]rc.OSWindow, error) {
	out, err := v.Run(ctx, v.Socket, "ls")
	if err != nil {
		return nil, err
	}
	var tree []rc.OSWindow
	if err := json.Unmarshal(out, &tree); err != nil {
		return nil, fmt.Errorf("parse ls: %w", err)
	}
	return tree, nil
}

// handleMatches collects EVERY window carrying the handle --
// FindWindowByUserVar is first-match-only and launch enforces no var
// uniqueness, so a failed re-stage makes a handle ambiguous; resolution
// must see that, not paper over it.
func handleMatches(tree []rc.OSWindow, handle string) []rc.Window {
	var out []rc.Window
	for _, osw := range tree {
		for _, tab := range osw.Tabs {
			for _, w := range tab.Windows {
				if w.UserVars[renoHandleVar] == handle {
					out = append(out, w)
				}
			}
		}
	}
	return out
}

func describeWins(wins []rc.Window) string {
	s := ""
	for i, w := range wins {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("window %d (%q)", w.ID, w.Title)
	}
	return s
}

// FocusSession focuses the window carrying handle: fresh ls, exactly one
// match, focus-window (never no_response: a zero-match focus with it
// still exits 0), then a post-action ls confirming focus actually moved.
func (v *RenoVerbs) FocusSession(ctx context.Context, handle string, corr RenoCorr) error {
	tree, err := v.freshLS(ctx)
	if err != nil {
		return v.Act(corr, "focus-session", handle, "error", err.Error(), err)
	}
	wins := handleMatches(tree, handle)
	switch {
	case len(wins) == 0:
		err := fmt.Errorf("no window carries %s=%s", renoHandleVar, handle)
		return v.Act(corr, "focus-session", handle, "miss", err.Error(), err)
	case len(wins) > 1:
		err := fmt.Errorf("handle %s is ambiguous: %s", handle, describeWins(wins))
		return v.Act(corr, "focus-session", handle, "ambiguous", err.Error(), err)
	}
	id := wins[0].ID
	if _, err := v.Run(ctx, v.Socket, "focus-window", "--match", "id:"+strconv.Itoa(id)); err != nil {
		return v.Act(corr, "focus-session", handle, "error", err.Error(), err)
	}
	after, err := v.freshLS(ctx)
	if err != nil {
		return v.Act(corr, "focus-session", handle, "unconfirmed",
			fmt.Sprintf("focus-window ok but confirm ls failed: %v", err), err)
	}
	if !windowFocused(after, id) {
		err := fmt.Errorf("focus-window id:%d ran but the window is not focused", id)
		return v.Act(corr, "focus-session", handle, "unconfirmed", err.Error(), err)
	}
	return v.Act(corr, "focus-session", handle, "ok", fmt.Sprintf("window %d", id), nil)
}

func windowFocused(tree []rc.OSWindow, id int) bool {
	for _, osw := range tree {
		for _, tab := range osw.Tabs {
			for _, w := range tab.Windows {
				if w.ID == id {
					return w.IsFocused
				}
			}
		}
	}
	return false
}

// LaunchSession ENSURES a main-kitty tab carrying the handle: exactly
// one already-planted window is success (prev/goto legally re-enter a
// launch step -- ensure-semantics keep the revisit from failing the
// show), two or more is the ambiguity every later focus would trip on,
// zero launches fresh. Post-flight confirms exactly one window carries
// the handle.
func (v *RenoVerbs) LaunchSession(ctx context.Context, handle, cwd string, keepFocus bool, argv []string, corr RenoCorr) error {
	tree, err := v.freshLS(ctx)
	if err != nil {
		return v.Act(corr, "launch-session", handle, "error", err.Error(), err)
	}
	switch wins := handleMatches(tree, handle); {
	case len(wins) == 1:
		return v.Act(corr, "launch-session", handle, "ok",
			fmt.Sprintf("already planted: window %d", wins[0].ID), nil)
	case len(wins) > 1:
		err := fmt.Errorf("handle %s already planted %d times: %s", handle, len(wins), describeWins(wins))
		return v.Act(corr, "launch-session", handle, "ambiguous", err.Error(), err)
	}
	args := []string{"launch", "--type", "tab", "--var", renoHandleVar + "=" + handle}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	if keepFocus {
		args = append(args, "--keep-focus")
	}
	args = append(args, argv...)
	if _, err := v.Run(ctx, v.Socket, args...); err != nil {
		return v.Act(corr, "launch-session", handle, "error", err.Error(), err)
	}
	after, err := v.freshLS(ctx)
	if err != nil {
		return v.Act(corr, "launch-session", handle, "unconfirmed",
			fmt.Sprintf("launch ok but confirm ls failed: %v", err), err)
	}
	wins := handleMatches(after, handle)
	if len(wins) != 1 {
		err := fmt.Errorf("launch ran but %d windows carry %s", len(wins), handle)
		return v.Act(corr, "launch-session", handle, "unconfirmed", err.Error(), err)
	}
	return v.Act(corr, "launch-session", handle, "ok", fmt.Sprintf("window %d", wins[0].ID), nil)
}

// OpenURL opens an http(s) URL via `open`, journaled like every step
// action.
func (v *RenoVerbs) OpenURL(ctx context.Context, url string, corr RenoCorr) error {
	if err := v.openStart(ctx, []string{"open", url}); err != nil {
		return v.Act(corr, "open-url", url, "error", err.Error(), err)
	}
	return v.Act(corr, "open-url", url, "ok", "", nil)
}

// CtlCall sends one reno ctl subverb to the Reno owner and returns its Data --
// the record verbs' handshake seam (arm before OBS, note after).
type CtlCall func(args []string) (json.RawMessage, error)

// RecordOpen mirrors the module's record-arm payload.
type RecordOpen struct {
	Run       string                 `json:"run"`
	Directory string                 `json:"directory"`
	Scene     string                 `json:"scene,omitempty"`
	Config    *renocore.RecordConfig `json:"config,omitempty"`
}

// Record is the recording start verb: arm over ctl (the module journals
// intent WRITE-AHEAD and hands back the run's clip directory), then
// point OBS at it and start. Failures note back so the journal owns the
// truth; the started note carries the OBS version (recording identity).
func (v *RenoVerbs) Record(dial func() (ObsTransport, error), ctl CtlCall, corr RenoCorr) error {
	raw, err := ctl([]string{"record-open", corr.Run})
	if err != nil {
		return v.Act(corr, "record", "", "error", "open: "+err.Error(), err)
	}
	var intent RecordOpen
	if err := json.Unmarshal(raw, &intent); err != nil {
		return v.Act(corr, "record", "", "error", "open decode: "+err.Error(), err)
	}
	corr.Run = intent.Run
	failNote := func(stage string, err error) error {
		if _, nerr := ctl([]string{"record-note", intent.Run, "failed", stage + ": " + err.Error()}); nerr != nil {
			err = errors.Join(err, nerr)
		}
		return v.Act(corr, "record", intent.Run, "error", stage+": "+err.Error(), err)
	}
	// phase one, named on glass. Record always ends up rolling, but when
	// OBS is cold it pays the launch first, and a pad that said nothing for
	// two minutes read as a dead button. Best effort: a phase note is
	// decoration, and losing it must never fail a take.
	phase := func(s string) {
		if _, err := ctl([]string{"record-phase", s}); err != nil {
			fmt.Fprintf(os.Stderr, "khudson reno record: phase note %q: %v\n", s, err)
		}
	}
	phase("standby")
	defer phase("")
	obs, err := dial()
	if err != nil {
		return failNote("dial", err)
	}
	defer obs.Close()
	if err := os.MkdirAll(intent.Directory, 0o700); err != nil {
		return failNote("workspace", err)
	}
	if obs.CanSetRecordDirectory() {
		if err := obs.SetRecordDirectory(intent.Directory); err != nil {
			return failNote("record-directory", err)
		}
	} else {
		fmt.Fprintln(os.Stderr, "khudson reno record: obs lacks SetRecordDirectory; clip lands in the OBS default directory (journal still owns the path at stop)")
	}
	switch {
	case intent.Config != nil && !intent.Config.Empty():
		// the one-core config path: ensure the reno scene matches and
		// switch to it (scripted headers, `reno config`, and view taps
		// all land here identically)
		dead, err := ApplyRecordConfig(obs, *intent.Config)
		if err != nil {
			return failNote("config ("+intent.Config.Summary()+")", err)
		}
		if len(dead) > 0 {
			// refuse rather than roll: a take over a dead capture is a file
			// full of black that reads as a successful recording everywhere
			// -- the journal, the pad, the clip tile -- until it is played
			return failNote("config ("+intent.Config.Summary()+")",
				fmt.Errorf("no frames from %s", strings.Join(dead, ", ")))
		}
	case intent.Scene != "":
		if err := obs.SetScene(intent.Scene); err != nil {
			return failNote("scene "+intent.Scene, err)
		}
	}
	if err := obs.StartRecord(); err != nil {
		return failNote("start", err)
	}
	if err := noteWithRetry(ctl, []string{"record-note", intent.Run, "started", obs.Version()}); err != nil {
		// the run-stop path still owns the orphan (stop-worthy covers
		// unproven starts), but the failure stays loud
		return v.Act(corr, "record", intent.Run, "unconfirmed",
			"recording live but the started note failed: "+err.Error(), err)
	}
	return v.Act(corr, "record", intent.Run, "ok", "obs "+obs.Version()+" -> "+intent.Directory, nil)
}

// ApplyRequest mirrors the module's apply-begin payload.
type ApplyRequest struct {
	Config *renocore.RecordConfig `json:"config,omitempty"`
}

// QuitRequest mirrors the module's quit-begin payload: the run a
// record-stop note belongs to, and whether a take is still open.
type QuitRequest struct {
	Run       string `json:"run,omitempty"`
	Recording bool   `json:"recording,omitempty"`
}

// ApplyConfig is the `khudson reno obs apply` verb: read the pad's
// current config over ctl, drive the reno scene to match, note the
// outcome back. The dial is DELIBERATELY the non-launching one -- a
// config tap is a tweak against OBS as it stands, and a stray tile must
// never cold-launch the app (record/ensure/probe own that).
//
// The verb reads the config at exec time rather than taking it as argv:
// taps that landed while this apply was queued are then already folded
// in, which is what makes the module's one-in-flight latch converge.
func (v *RenoVerbs) ApplyConfig(dial func() (ObsTransport, error), ctl CtlCall, corr RenoCorr) error {
	fail := func(detail string, err error) error {
		if _, nerr := ctl([]string{"apply-note", "error", detail}); nerr != nil {
			err = errors.Join(err, nerr)
		}
		return v.Act(corr, "obs", "apply", "error", detail, err)
	}
	raw, err := ctl([]string{"apply-begin"})
	if err != nil {
		return v.Act(corr, "obs", "apply", "error", "begin: "+err.Error(), err)
	}
	var req ApplyRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return fail("begin decode: "+err.Error(), err)
	}
	if req.Config == nil {
		// the config emptied out between emission and exec: nothing to
		// drive, and an empty plan is refused by contract
		if _, err := ctl([]string{"apply-note", "ok", "unconfigured (obs untouched)"}); err != nil {
			return v.Act(corr, "obs", "apply", "unconfirmed", "empty config but the note failed: "+err.Error(), err)
		}
		return v.Act(corr, "obs", "apply", "ok", "empty config; obs untouched", nil)
	}
	obs, err := dial()
	if err != nil {
		// OBS down is the ordinary state between takes, not a fault: the
		// pad says cold and the knob still holds for the next record, so
		// this notes ok -- an alarm here would cry wolf every session
		if _, nerr := ctl([]string{"apply-note", "ok", "obs cold; held for record"}); nerr != nil {
			return v.Act(corr, "obs", "apply", "error", "obs cold and the note failed: "+nerr.Error(), errors.Join(err, nerr))
		}
		return v.Act(corr, "obs", "apply", "ok", "obs cold; config held for the next record", nil)
	}
	defer obs.Close()
	dead, err := ApplyRecordConfig(obs, *req.Config)
	if err != nil {
		return fail(err.Error(), err)
	}
	detail := req.Config.Summary()
	if len(dead) > 0 {
		// apply is a knob turn, not a gate -- it still landed, and refusing
		// would strand the config. Say it loudly on the pad instead.
		detail += " -- NO FRAMES from " + strings.Join(dead, ", ")
		if nerr := noteWithRetry(ctl, []string{"apply-note", "error", detail}); nerr != nil {
			return v.Act(corr, "obs", "apply", "unconfirmed", detail+"; note failed: "+nerr.Error(), nerr)
		}
		return v.Act(corr, "obs", "apply", "error", detail, errors.New(detail))
	}
	if err := noteWithRetry(ctl, []string{"apply-note", "ok", detail}); err != nil {
		return v.Act(corr, "obs", "apply", "unconfirmed",
			"scene applied ("+detail+") but the note failed: "+err.Error(), err)
	}
	return v.Act(corr, "obs", "apply", "ok", detail, nil)
}

// Notes are the journal's only record of started/stopped state. Retry once
// so a transient control connection failure does not orphan a recording.
func noteWithRetry(ctl CtlCall, args []string) error {
	_, err := ctl(args)
	if err == nil {
		return nil
	}
	time.Sleep(time.Second)
	_, err2 := ctl(args)
	if err2 == nil {
		return nil
	}
	return errors.Join(err, err2)
}

// ObsOp runs one transport operation (stop|pause|resume|scene). Stop
// notes the clip's output path back into the run journal -- the
// deterministic clip association.
func (v *RenoVerbs) ObsOp(op, arg string, dial func() (ObsTransport, error), ctl CtlCall, corr RenoCorr) error {
	target := op
	if arg != "" {
		target = op + " " + arg
	}
	obs, err := dial()
	if err != nil {
		return v.Act(corr, "obs", target, "error", err.Error(), err)
	}
	defer obs.Close()
	switch op {
	case "stop":
		path, err := obs.StopRecord()
		if err != nil {
			if isOutputNotRunning(err) {
				// A lost stopped note leaves the journal active after OBS stops.
				// Close that run without claiming a clip path we cannot recover.
				if _, nerr := ctl([]string{"record-note", corr.Run, "stopped"}); nerr != nil {
					return v.Act(corr, "obs", target, "error",
						"no active recording and the stopped note failed: "+nerr.Error(), errors.Join(err, nerr))
				}
				return v.Act(corr, "obs", target, "ok", "no active recording; journal closed without a clip path", nil)
			}
			return v.Act(corr, "obs", target, "error", err.Error(), err)
		}
		if err := noteWithRetry(ctl, []string{"record-note", corr.Run, "stopped", path}); err != nil {
			return v.Act(corr, "obs", target, "unconfirmed",
				"recording stopped ("+path+") but the note failed: "+err.Error(), err)
		}
		return v.Act(corr, "obs", target, "ok", path, nil)
	case "pause":
		err = obs.PauseRecord()
	case "resume":
		err = obs.ResumeRecord()
	case "scene":
		err = obs.SetScene(arg)
	default:
		err = fmt.Errorf("unknown obs op %q", op)
	}
	if err != nil {
		return v.Act(corr, "obs", target, "error", err.Error(), err)
	}
	return v.Act(corr, "obs", target, "ok", "", nil)
}

// isOutputNotRunning matches obs-websocket's OutputNotRunning refusal
// (code 501): StopRecord against an idle OBS. String-matched because
// goobs surfaces the code only in the message text.
func isOutputNotRunning(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not running") || strings.Contains(s, "not active") || strings.Contains(s, "501")
}

// chromeTabScript activates the first Chrome tab whose URL or title
// contains the needle. AppleScript over the Scripting Bridge sdef -- the
// CDP path died on the daily profile at Chrome 136, and chrome-cli is
// unpackaged, so osascript is the dispatcher of record (gate 3). Needs
// the one-time Automation (AppleEvents -> Chrome) grant on the khudson
// binary.
const chromeTabScript = `on run argv
	set needle to item 1 of argv
	tell application "Google Chrome"
		set wi to 0
		repeat with w in windows
			set wi to wi + 1
			set ti to 0
			repeat with t in tabs of w
				set ti to ti + 1
				if (URL of t contains needle) or (title of t contains needle) then
					set active tab index of w to ti
					set index of w to 1
					activate
					return "window " & wi & " tab " & ti
				end if
			end repeat
		end repeat
	end tell
	error "no chrome tab matches " & needle
end run`

// With -e, osascript passes remaining arguments to the run handler. A
// literal "-" separator would become argv[0], displacing the search string.
var osaRun = func(ctx context.Context, script string, args ...string) (string, error) {
	argv := append([]string{"-e", script}, args...)
	out, err := subprocess.Output(ctx, "osascript", argv...)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			msg, _, _ := strings.Cut(strings.TrimSpace(string(ee.Stderr)), "\n")
			return "", fmt.Errorf("%s: %w", msg, err)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ChromeTab activates the first tab matching needle (URL or title
// substring), journaled like every step action.
func (v *RenoVerbs) ChromeTab(ctx context.Context, needle string, corr RenoCorr) error {
	where, err := osaRun(ctx, chromeTabScript, needle)
	if err != nil {
		outcome := "error"
		if strings.Contains(err.Error(), "no chrome tab matches") {
			outcome = "miss"
		}
		return v.Act(corr, "chrome-tab", needle, outcome, err.Error(), err)
	}
	return v.Act(corr, "chrome-tab", needle, "ok", where, nil)
}
