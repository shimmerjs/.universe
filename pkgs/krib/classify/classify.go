// Package classify assigns entries to sheet-configured groups: field-scoped
// RE2 match against Entry.Cmd plus exact-cmd lists, all-matching-groups
// membership, one optional catch-all group per sheet, and a named-sorter
// registry for deterministic in-group ordering.
package classify

import (
	"fmt"
	"regexp"

	"github.com/shimmerjs/krib/envelope"
)

// Sheet is the classification config for one sheet. It is the Go shape the
// sheets/*.cue exports decode into (see the sheets package loader).
type Sheet struct {
	Name string `json:"name"`
	// Sort is the default sorter chain applied to every group; nil means
	// DefaultSort. Group.Sort overrides it wholesale.
	Sort []string `json:"sort,omitempty"`
	// Exec is the sheet-level accept behavior; nil means entries are not
	// runnable. EntryRule.Exec overrides it per entry.
	Exec   *ExecSpec   `json:"exec,omitempty"`
	Theme  *Theme      `json:"theme,omitempty"`
	Groups []GroupSpec `json:"groups,omitempty"`
	// Entries are per-entry rules matched against Entry.Cmd; the first
	// matching rule wins.
	Entries []EntryRule `json:"entries,omitempty"`
}

// GroupSpec declares one group. A nil Match marks the catch-all group that
// receives entries no other group matched; at most one per sheet.
type GroupSpec struct {
	Name   string   `json:"name"`
	Key    string   `json:"key,omitempty"`    // palette group-filter hotkey
	Header bool     `json:"header,omitempty"` // presentation hint: render in the header area
	Pin    bool     `json:"pin,omitempty"`    // featured: print places pinned groups first
	Match  *Match   `json:"match,omitempty"`
	Sort   []string `json:"sort,omitempty"`
}

// Match is matched against Entry.Cmd: the RE2 pattern OR exact membership.
type Match struct {
	Re    string   `json:"re,omitempty"`
	Exact []string `json:"exact,omitempty"`
}

// ExecSpec declares how an accepted entry runs. Run is one of "run" (spawn
// Argv, no shell), "copy" (entry command to the clipboard), or "none".
// In Argv, the element "{cmd}" is replaced by the entry's raw Cmd string as
// one argument; an element containing "{window}" has it replaced by the
// target window id, or is dropped entirely when no target is given.
type ExecSpec struct {
	Run  string   `json:"run"`
	Argv []string `json:"argv,omitempty"`
}

// EntryRule attaches per-entry behavior by Cmd match: a confirm-before-run
// flag and/or an ExecSpec override.
type EntryRule struct {
	Match   Match     `json:"match"`
	Confirm bool      `json:"confirm,omitempty"`
	Exec    *ExecSpec `json:"exec,omitempty"`
}

// Theme is the print palette and layout; zero-valued fields fall back to the
// render defaults (the old hardcoded constants).
type Theme struct {
	Keys      string `json:"keys,omitempty"`
	Cmd       string `json:"cmd,omitempty"`
	Header    string `json:"header,omitempty"`
	RowSep    string `json:"rowSep,omitempty"`
	Dim       string `json:"dim,omitempty"`
	LeftWidth int    `json:"leftWidth,omitempty"`
}

// Rule returns the first entry rule whose Match hits cmd, or nil.
func (s Sheet) Rule(cmd string) *EntryRule {
	for i, r := range s.Entries {
		if r.Match.Re != "" {
			if re, err := regexp.Compile(r.Match.Re); err == nil && re.MatchString(cmd) {
				return &s.Entries[i]
			}
		}
		for _, e := range r.Match.Exact {
			if e == cmd {
				return &s.Entries[i]
			}
		}
	}
	return nil
}

// VetSheet validates a sheet config independent of any data: group set
// coherence, RE2 patterns, sorter names, and exec vocabulary. Loaders call
// it so config errors surface at load, not first use.
func VetSheet(s Sheet) error {
	if _, err := Classify(s, &envelope.Envelope{}); err != nil {
		return err
	}
	if s.Sort != nil {
		if _, err := Sorters(s.Sort); err != nil {
			return fmt.Errorf("sheet %s: %w", s.Name, err)
		}
	}
	if err := vetExec(s.Exec); err != nil {
		return fmt.Errorf("sheet %s: %w", s.Name, err)
	}
	for i, r := range s.Entries {
		if r.Match.Re != "" {
			if _, err := regexp.Compile(r.Match.Re); err != nil {
				return fmt.Errorf("sheet %s: entry rule %d: %w", s.Name, i, err)
			}
		}
		if r.Match.Re == "" && len(r.Match.Exact) == 0 {
			return fmt.Errorf("sheet %s: entry rule %d has an empty match", s.Name, i)
		}
		if err := vetExec(r.Exec); err != nil {
			return fmt.Errorf("sheet %s: entry rule %d: %w", s.Name, i, err)
		}
	}
	return nil
}

func vetExec(e *ExecSpec) error {
	if e == nil {
		return nil
	}
	switch e.Run {
	case "run":
		if len(e.Argv) == 0 {
			return fmt.Errorf("exec run %q with empty argv", e.Run)
		}
	case "copy", "none":
	default:
		return fmt.Errorf("unknown exec run %q (want run, copy, or none)", e.Run)
	}
	return nil
}

// Grouped is one group with its classified, sorted entries.
type Grouped struct {
	Name    string
	Key     string
	Header  bool
	Pin     bool
	Meta    envelope.GroupMeta
	Entries []envelope.Entry
}

// Classify assigns every entry to all matching groups, in sheet group order.
// Entries matching no group land in the catch-all; with no catch-all
// configured an unmatched entry is an error (loud beats silently dropped).
// Group metadata is joined in from env.Groups by name when present.
func Classify(sheet Sheet, env *envelope.Envelope) ([]Grouped, error) {
	type compiled struct {
		spec  GroupSpec
		re    *regexp.Regexp
		exact map[string]bool
	}

	var catchAll = -1
	comps := make([]compiled, 0, len(sheet.Groups))
	names := make(map[string]bool, len(sheet.Groups))
	for i, g := range sheet.Groups {
		if names[g.Name] {
			return nil, fmt.Errorf("sheet %s: duplicate group %q", sheet.Name, g.Name)
		}
		names[g.Name] = true
		if _, err := sorters(sheet, g); err != nil {
			return nil, fmt.Errorf("sheet %s: group %q: %w", sheet.Name, g.Name, err)
		}
		c := compiled{spec: g}
		if g.Match == nil {
			if catchAll >= 0 {
				return nil, fmt.Errorf("sheet %s: more than one catch-all group (%q and %q)",
					sheet.Name, sheet.Groups[catchAll].Name, g.Name)
			}
			catchAll = i
		} else {
			if g.Match.Re != "" {
				re, err := regexp.Compile(g.Match.Re)
				if err != nil {
					return nil, fmt.Errorf("sheet %s: group %q: %w", sheet.Name, g.Name, err)
				}
				c.re = re
			}
			c.exact = make(map[string]bool, len(g.Match.Exact))
			for _, cmd := range g.Match.Exact {
				c.exact[cmd] = true
			}
		}
		comps = append(comps, c)
	}

	meta := make(map[string]envelope.GroupMeta, len(env.Groups))
	for _, g := range env.Groups {
		meta[g.Name] = g.Meta
	}

	buckets := make([][]envelope.Entry, len(comps))
	for _, en := range env.Entries {
		matched := false
		for i, c := range comps {
			if c.spec.Match == nil {
				continue
			}
			if (c.re != nil && c.re.MatchString(en.Cmd)) || c.exact[en.Cmd] {
				matched = true
				buckets[i] = append(buckets[i], en)
			}
		}
		if !matched {
			if catchAll < 0 {
				return nil, fmt.Errorf("sheet %s: entry %q matches no group and there is no catch-all",
					sheet.Name, en.ID(env.Kind))
			}
			buckets[catchAll] = append(buckets[catchAll], en)
		}
	}

	out := make([]Grouped, len(comps))
	for i, c := range comps {
		ss, _ := sorters(sheet, c.spec) // vetted above
		for _, s := range ss {
			s(buckets[i])
		}
		out[i] = Grouped{
			Name:    c.spec.Name,
			Key:     c.spec.Key,
			Header:  c.spec.Header,
			Pin:     c.spec.Pin,
			Meta:    meta[c.spec.Name],
			Entries: buckets[i],
		}
	}
	return out, nil
}

// ByGroup groups a cards envelope by its data-borne Entry.Group field, in
// env.Groups order; undeclared groups follow in order of first appearance.
func ByGroup(env *envelope.Envelope) []Grouped {
	idx := make(map[string]int, len(env.Groups))
	out := make([]Grouped, 0, len(env.Groups))
	for _, g := range env.Groups {
		idx[g.Name] = len(out)
		out = append(out, Grouped{Name: g.Name, Meta: g.Meta})
	}
	for _, en := range env.Entries {
		i, ok := idx[en.Group]
		if !ok {
			i = len(out)
			idx[en.Group] = i
			out = append(out, Grouped{Name: en.Group})
		}
		out[i].Entries = append(out[i].Entries, en)
	}
	return out
}

func sorters(sheet Sheet, g GroupSpec) ([]Sorter, error) {
	names := g.Sort
	if names == nil {
		names = sheet.Sort
	}
	if names == nil {
		names = DefaultSort
	}
	return Sorters(names)
}
