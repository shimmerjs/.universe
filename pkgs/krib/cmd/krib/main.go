// krib renders, lists, and executes krib sheets from an envelope (or raw
// kitten JSONL) on stdin:
//
//	krib print [--sheet name|path] [--filter name] [--from auto|envelope|kitty-jsonl] [--width n] < data
//	krib list [--sheet name|path] [--json] [--all] [--recent] [--from ...] < data
//	krib exec [--data file] [--sheet name|path] [--window id] [--yes] <id>
//	krib palette --data file [--sheet name|path] [--window id] [--all]
//
// print is a static ANSI dump, pager-clean (pipe to less -R); list emits flat
// entries with stable ids for fzf; exec resolves one accepted id from the
// session cache and runs it through the sheet's exec descriptor; palette is
// the fzf frontend over list + exec.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/shimmerjs/krib/adapter/kittyjsonl"
	"github.com/shimmerjs/krib/chord"
	"github.com/shimmerjs/krib/classify"
	"github.com/shimmerjs/krib/envelope"
	"github.com/shimmerjs/krib/sheets"
	"github.com/shimmerjs/krib/state"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "krib:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: krib print|list|exec|palette [flags]")
	}
	switch args[0] {
	case "print":
		return runPrint(args[1:])
	case "list":
		return runList(args[1:])
	case "exec":
		return runExec(args[1:])
	case "palette":
		return runPalette(args[1:])
	default:
		return fmt.Errorf("unknown command %q (want print, list, exec, or palette)", args[0])
	}
}

func runPrint(args []string) error {
	fs := flag.NewFlagSet("print", flag.ContinueOnError)
	filter := fs.String("filter", "", "only groups whose name contains this (case-insensitive)")
	from := fs.String("from", "auto", "input format: auto, envelope, kitty-jsonl")
	width := fs.Int("width", 80, "render width in cells")
	sheetFlag := fs.String("sheet", "", "sheet config: embedded name or JSON file path (default kitty)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// F4 parity: the old clod-cheat filter was positional.
	if *filter == "" && fs.NArg() > 0 {
		*filter = fs.Arg(0)
	}

	env, err := decode(*from, os.Stdin)
	if err != nil {
		return err
	}
	sheet, err := sheets.Load(*sheetFlag)
	if err != nil {
		return err
	}
	observeState(env, time.Now())
	groups, err := groupsFor(env, sheet)
	if err != nil {
		return err
	}
	groups = pinFirst(groups)
	groups = filterGroups(groups, *filter)
	out := render(env, groups, *width, sheet.Theme)
	_, err = io.WriteString(os.Stdout, out)
	return err
}

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit one JSON object per entry")
	from := fs.String("from", "auto", "input format: auto, envelope, kitty-jsonl")
	sheetFlag := fs.String("sheet", "", "sheet config: embedded name or JSON file path (default kitty)")
	all := fs.Bool("all", false, "include entries only the catch-all group matched")
	recent := fs.Bool("recent", false, "only recently-changed entries, most recent first")
	window := fs.Duration("recent-window", 14*24*time.Hour, "recently-changed window")
	if err := fs.Parse(args); err != nil {
		return err
	}

	env, err := decode(*from, os.Stdin)
	if err != nil {
		return err
	}
	sheet, err := sheets.Load(*sheetFlag)
	if err != nil {
		return err
	}
	now := time.Now()
	st := observeState(env, now)
	entries, err := buildList(env, sheet, st, listOpts{
		all: *all, recent: *recent, recentWindow: *window, now: now,
	})
	if err != nil {
		return err
	}
	return writeList(os.Stdout, entries, *asJSON)
}

type listOpts struct {
	all          bool
	recent       bool
	recentWindow time.Duration
	now          time.Time
}

type listEntry struct {
	id      string
	display string
	detail  string
	groups  []string
	mode    string
	keys    string
	group   string
	term    string
	body    string
	cmd     string
	since   time.Time
	changed bool
}

// buildList flattens the envelope into palette entries: group membership
// comes from classifying against the sheet (bindings) or the data-borne
// group (cards). Entries only the catch-all matched are omitted unless
// opts.all. The changed marker keys off an OBSERVED value change (since
// moved past first-seen) within the window; opts.recent filters to those,
// most recent first.
func buildList(env *envelope.Envelope, sheet classify.Sheet, st *state.File, opts listOpts) ([]listEntry, error) {
	memberships := make(map[string][]string, len(env.Entries))
	catchOnly := make(map[string]bool, len(env.Entries))
	if env.Kind == envelope.KindBindings {
		groups, err := classify.Classify(sheet, env)
		if err != nil {
			return nil, err
		}
		catchAll := ""
		for _, g := range sheet.Groups {
			if g.Match == nil {
				catchAll = g.Name
			}
		}
		for _, g := range groups {
			for _, e := range g.Entries {
				id := e.ID(env.Kind)
				memberships[id] = append(memberships[id], g.Name)
				if g.Name == catchAll {
					catchOnly[id] = true
				}
			}
		}
		for id, gs := range memberships {
			if len(gs) > 1 {
				catchOnly[id] = false
			}
		}
	}

	out := make([]listEntry, 0, len(env.Entries))
	for _, en := range env.Entries {
		id := en.ID(env.Kind)
		if !opts.all && catchOnly[id] {
			continue
		}
		groups := memberships[id]
		if env.Kind != envelope.KindBindings {
			groups = []string{en.Group}
		}
		display := en.Term
		if env.Kind == envelope.KindBindings {
			display = chord.FormatSeq(en.Keys)
			if en.Mode != "" && en.Mode != "default" {
				display = "[" + en.Mode + "] " + display
			}
		}
		detail := en.Cmd
		if strings.TrimSpace(detail) == "" {
			// whitespace-only Cmd is as empty as empty: fall to the Body
			detail = en.Body
		}
		le := listEntry{
			id:      id,
			display: display,
			detail:  detail,
			groups:  groups,
			mode:    en.Mode,
			keys:    keysOrEmpty(en),
			group:   en.Group,
			term:    en.Term,
			body:    en.Body,
			cmd:     en.Cmd,
		}
		if se, ok := st.Entries[id]; ok {
			le.since = se.Since
			le.changed = se.Since.After(se.FirstSeen) && opts.now.Sub(se.Since) <= opts.recentWindow
		}
		if opts.recent && !le.changed {
			continue
		}
		out = append(out, le)
	}
	if opts.recent {
		slices.SortStableFunc(out, func(a, b listEntry) int {
			return b.since.Compare(a.since)
		})
	}
	return out, nil
}

func writeList(w io.Writer, entries []listEntry, asJSON bool) error {
	enc := json.NewEncoder(w)
	for _, le := range entries {
		if asJSON {
			if err := enc.Encode(flatEntry{
				ID:      le.id,
				Display: le.display,
				Mode:    le.mode,
				Keys:    le.keys,
				Group:   le.group,
				Groups:  le.groups,
				Term:    le.term,
				Body:    le.body,
				Cmd:     le.cmd,
				Since:   le.since,
				Changed: le.changed,
			}); err != nil {
				return err
			}
			continue
		}
		display := le.display
		if le.changed {
			display = "* " + display
		}
		// EVERY column holds the one-line/4-column contract, not just
		// detail: a tab or newline in any field silently shifts columns
		// for the fzf consumer (wave-5 review). Vet rejects control
		// whitespace in names too; this is the defensive layer.
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			col(le.id), col(display), col(strings.Join(le.groups, ",")), col(le.detail)); err != nil {
			return err
		}
	}
	return nil
}

// firstLine is the trimmed first non-empty line of s.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// col fits one value into the tab-separated line contract: first line
// only, interior tabs flattened to spaces.
func col(s string) string {
	return strings.ReplaceAll(firstLine(s), "\t", " ")
}

type flatEntry struct {
	ID      string    `json:"id"`
	Display string    `json:"display"`
	Mode    string    `json:"mode,omitempty"`
	Keys    string    `json:"keys,omitempty"`
	Group   string    `json:"group,omitempty"`
	Groups  []string  `json:"groups,omitempty"`
	Term    string    `json:"term,omitempty"`
	Body    string    `json:"body,omitempty"`
	Cmd     string    `json:"cmd,omitempty"`
	Since   time.Time `json:"since,omitzero"`
	Changed bool      `json:"changed,omitempty"`
}

func keysOrEmpty(en envelope.Entry) string {
	if len(en.Keys) == 0 {
		return ""
	}
	return chord.CanonicalSeq(en.Keys)
}

// decode reads one sheet from r. auto sniffs: a single JSON document carrying
// schemaVersion is an envelope, anything else is tried as kitten JSONL.
func decode(from string, r io.Reader) (*envelope.Envelope, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, fmt.Errorf("empty input: pipe a krib envelope or kitten JSONL to stdin")
	}
	switch from {
	case "envelope":
		return decodeEnvelope(b)
	case "kitty-jsonl":
		return kittyjsonl.Decode(bytes.NewReader(b))
	case "auto":
		var probe struct {
			SchemaVersion *int `json:"schemaVersion"`
		}
		if err := json.Unmarshal(b, &probe); err == nil && probe.SchemaVersion != nil {
			return decodeEnvelope(b)
		}
		return kittyjsonl.Decode(bytes.NewReader(b))
	default:
		return nil, fmt.Errorf("unknown --from %q (want auto, envelope, or kitty-jsonl)", from)
	}
}

// decodeData decodes from a file path (the session cache) or stdin.
func decodeData(path, from string) (*envelope.Envelope, error) {
	if path == "" {
		return decode(from, os.Stdin)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return decode(from, f)
}

func decodeEnvelope(b []byte) (*envelope.Envelope, error) {
	env, warnings, err := envelope.Decode(bytes.NewReader(b))
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "krib: warning:", w)
	}
	return env, err
}

// observeState folds the envelope into the statefile for env's sheet in one
// locked load-observe-save cycle (concurrent instances share the file).
// Resolution or update failures warn and degrade to in-memory state;
// observation never blocks output.
func observeState(env *envelope.Envelope, now time.Time) *state.File {
	if p, err := state.Path(env.Meta.Sheet); err == nil {
		st, err := state.Update(p, func(f *state.File) bool {
			return f.Observe(env, now)
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "krib: warning: statefile:", err)
		}
		if st != nil {
			return st
		}
	}
	st := state.New()
	st.Observe(env, now)
	return st
}

// groupsFor classifies bindings against the sheet config and groups cards by
// their data-borne group field.
func groupsFor(env *envelope.Envelope, sheet classify.Sheet) ([]classify.Grouped, error) {
	if env.Kind == envelope.KindCards {
		return classify.ByGroup(env), nil
	}
	return classify.Classify(sheet, env)
}

// pinFirst stably reorders pinned (featured) groups ahead of the rest.
func pinFirst(groups []classify.Grouped) []classify.Grouped {
	out := make([]classify.Grouped, 0, len(groups))
	for _, g := range groups {
		if g.Pin {
			out = append(out, g)
		}
	}
	for _, g := range groups {
		if !g.Pin {
			out = append(out, g)
		}
	}
	return out
}

func filterGroups(groups []classify.Grouped, filter string) []classify.Grouped {
	if filter == "" {
		return groups
	}
	f := strings.ToLower(filter)
	var out []classify.Grouped
	for _, g := range groups {
		if strings.Contains(strings.ToLower(g.Name), f) {
			out = append(out, g)
		}
	}
	return out
}
