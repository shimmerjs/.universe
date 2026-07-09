// Package classify assigns entries to sheet-configured groups: field-scoped
// RE2 match against Entry.Cmd plus exact-cmd lists, all-matching-groups
// membership, one optional catch-all group per sheet, and a named-sorter
// registry for deterministic in-group ordering.
package classify

import (
	"fmt"
	"regexp"

	"github.com/shimmerjs/kittykrib/envelope"
)

// Sheet is the classification config for one sheet. It is the Go shape the
// future sheets/*.cue exports decode into.
type Sheet struct {
	Name string
	// Sort is the default sorter chain applied to every group; nil means
	// DefaultSort. Group.Sort overrides it wholesale.
	Sort   []string
	Groups []GroupSpec
}

// GroupSpec declares one group. A nil Match marks the catch-all group that
// receives entries no other group matched; at most one per sheet.
type GroupSpec struct {
	Name   string
	Key    string // keyboard-shell hotkey; carried through, unused here
	Header bool   // presentation hint: render in the header area
	Match  *Match
	Sort   []string
}

// Match is matched against Entry.Cmd: the RE2 pattern OR exact membership.
type Match struct {
	Re    string
	Exact []string
}

// Grouped is one group with its classified, sorted entries.
type Grouped struct {
	Name    string
	Key     string
	Header  bool
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
