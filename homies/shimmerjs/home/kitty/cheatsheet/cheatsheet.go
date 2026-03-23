package main

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/viewport"
)

type kribNotes struct {
	// all (via stdin)
	// user-configured (read file, hardcoded)
	kmod       []string
	categories []*category

	width    int
	filter   string // active category name, empty = show all
	viewport viewport.Model
	ready    bool
}

func (k *kribNotes) getCategory(n string) *category {
	for _, c := range k.categories {
		if c.name == n {
			return c
		}
	}
	return nil
}

type category struct {
	name     string
	selector *categorySelector
	binds    []action // can have multiple bindings per action
	key      string
	sort     func([]action) // optional sort for category output ordering
	header   bool           // render in header area instead of main layout
}

func (c *category) addBind(b *bind) {
	for i := range c.binds {
		if c.binds[i].name == b.action {
			c.binds[i].binds = append(c.binds[i].binds, b)
			return
		}
	}
	c.binds = append(c.binds, action{name: b.action, binds: []*bind{b}})
}

func (c *category) rows() [][]string {
	sortByLeadingKey(c.binds)
	sortLongestLast(c.binds)
	sortGroupNextPrev(c.binds)
	if c.sort != nil {
		c.sort(c.binds)
	}
	rows := make([][]string, len(c.binds))
	for i, a := range c.binds {
		rows[i] = a.row()
	}
	return rows
}

func (c *category) match(b *bind) bool {
	if c.selector == nil {
		return false
	}
	return c.selector.match(b)
}

type categorySelector struct {
	re      string
	actions []string
}

func (s *categorySelector) match(b *bind) bool {
	ok, err := regexp.MatchString(s.re, b.action)
	if err != nil {
		panic(err)
	}
	if ok {
		return ok
	}

	return slices.Contains(s.actions, b.action)
}

type bind struct {
	mode   string
	keys   []string
	action string
}

var keyGlyphs = map[string]string{
	"cmd":   "\u2318",
	"super": "\u2318",
	"alt":   "\u2325",
	"opt":   "\u2325",
	"ctrl":  "\u2303",
	"shift": "\u21E7",
}

func formatKey(k string) string {
	if g, ok := keyGlyphs[strings.ToLower(k)]; ok {
		return g
	}
	return k
}

func formatKeys(keys []string) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = formatKey(k)
	}
	return strings.Join(parts, " + ")
}

func (b *bind) keyStr() string {
	return formatKeys(b.keys)
}

func actionIntKey(a action) (int, bool) {
	for _, b := range a.binds {
		for _, k := range b.keys {
			if n, err := strconv.Atoi(k); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

func actionGroupKey(a action) string {
	name := a.name
	for _, prefix := range []string{"next_", "previous_"} {
		if base, ok := strings.CutPrefix(name, prefix); ok {
			return base
		}
	}
	return name
}

func isNextPrev(a action) bool {
	return strings.HasPrefix(a.name, "next_") || strings.HasPrefix(a.name, "previous_")
}

func sortGroupNextPrev(aa []action) {
	slices.SortStableFunc(aa, func(a, b action) int {
		ap, bp := isNextPrev(a), isNextPrev(b)
		if ap && bp {
			return strings.Compare(actionGroupKey(a), actionGroupKey(b))
		}
		if ap {
			return -1
		}
		if bp {
			return 1
		}
		return 0
	})
}

func actionLeadingKey(a action) string {
	if len(a.binds) > 0 {
		keys := a.binds[0].keys
		if len(keys) > 0 {
			return strings.ToLower(keys[0])
		}
	}
	return ""
}

func sortByLeadingKey(aa []action) {
	slices.SortStableFunc(aa, func(a, b action) int {
		return strings.Compare(actionLeadingKey(a), actionLeadingKey(b))
	})
}

func sortLongestLast(aa []action) {
	slices.SortStableFunc(aa, func(a, b action) int {
		return len(a.name) - len(b.name)
	})
}

func sortIntKeysLast(aa []action) {
	slices.SortStableFunc(aa, func(a, b action) int {
		an, aok := actionIntKey(a)
		bn, bok := actionIntKey(b)
		if !aok && !bok {
			return 0
		}
		if !aok {
			return -1
		}
		if !bok {
			return 1
		}
		return an - bn
	})
}

type action struct {
	name  string
	binds []*bind
}

func (a action) row() []string {
	bstr := make([]string, 0, len(a.binds))
	for _, x := range a.binds {
		bstr = append(bstr, x.keyStr())
	}
	return []string{strings.Join(bstr, "\n"), a.name}
}

