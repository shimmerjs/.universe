// Reno action verbs: the `khudson reno focus-session|launch-session|
// open-url` wrappers that the bus effect executor (and a bare shell)
// runs. Same discipline as the claude verbs -- a FRESH `kitten @ ls`
// before acting, resolution at exec time, socket-file auth -- plus the
// deltas the demo engine needs: resolution by the reno_handle user var
// with duplicate handles rejected loudly, a post-action read confirming
// the act landed, and correlated structured events (run id + step +
// action index) journaled to <state root>/reno/log/events.jsonl. Journal
// write failures FAIL THE VERB (nonzero exit -> ActFail warn cell):
// wait guards and post-mortems read this journal, so a silent miss is
// worse than a dead step.
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
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/rc"
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
	f, err := os.OpenFile(v.EventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
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

// LaunchSession launches a new main-kitty tab planting the handle.
// Pre-flight rejects an already-planted handle (launching a duplicate
// would make every later focus ambiguous); post-flight confirms exactly
// one window now carries it.
func (v *RenoVerbs) LaunchSession(ctx context.Context, handle, cwd string, keepFocus bool, argv []string, corr RenoCorr) error {
	tree, err := v.freshLS(ctx)
	if err != nil {
		return v.Act(corr, "launch-session", handle, "error", err.Error(), err)
	}
	if wins := handleMatches(tree, handle); len(wins) > 0 {
		err := fmt.Errorf("handle %s already planted: %s (focus it or pick another handle)", handle, describeWins(wins))
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
