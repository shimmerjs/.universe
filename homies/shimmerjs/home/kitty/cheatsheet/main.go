package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"os"
	"regexp"
	"slices"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

// todo: probably get wired up into bubbletea before fucking with layout anymore

// todo: add ordering for specific actions
// todo: add kitty mod at the top
// todo: sort based on keys (eg put f keys together if they are in same category)
// todo: flat list mode for fzf friendlyness
// todo: sort some categories by length of action so they are at the bottom
// todo: style for 'long action' which adds more margin
// todo: parse out action aliases from config
// todo: accept config file
// todo: highlights/common section with specific commands featured
// todo: handle non-default modes?

var categories = []*category{
	{
		name: "windows",
		key:  "w",
		selector: &categorySelector{
			re: "window",
		},
	},
	{
		name: "tabs",
		key:  "t",
		selector: &categorySelector{
			re: "tab",
		},
	},
	{
		name: "layout",
		key:  "l",
		selector: &categorySelector{
			re: "layout",
		},
	},
	{
		name: "scrolling",
		key:  "m",
		selector: &categorySelector{
			re: "^scroll_|show_scrollback|show_.*_command_output|clear_terminal",
		},
	},
	{
		name: "system",
		key:  "s",
		selector: &categorySelector{
			re:      "config|macos",
			actions: []string{"quit"},
		},
	},
	{
		name: "clipboard",
		key:  "c",
		selector: &categorySelector{
			re: "clipboard",
			actions: []string{
				"copy_or_noop",
				"paste_from_selection",
				"pass_selection_from_program",
			},
		},
	},
	{
		name: "other",
		key:  "o",
	},
}

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
	binds    actions // can have multiple bindings per action
	key      string
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

type actions map[string][]*bind

func (actions actions) rows() [][]string {
	return slices.Collect(actions.iter())
}

func (actions actions) iter() iter.Seq[[]string] {
	return func(yield func([]string) bool) {
		for a, binds := range actions {
			bstr := make([]string, 0, len(binds))
			for _, x := range binds {
				bstr = append(bstr, x.keyStr())
			}
			if !yield([]string{strings.Join(bstr, "\n"), a}) {
				return
			}
		}
	}
}

// '\u2318'

func main() {
	if err := run(); err != nil {
		fmt.Fprint(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	k, err := newSheet()
	if err != nil {
		return err
	}

	_, err = tea.NewProgram(k).Run()
	return err
}

func newSheet() (*kribNotes, error) {
	var d []byte
	d, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, err
	}
	if len(d) == 0 {
		return nil, fmt.Errorf("must provide keybindings on stdin")
	}

	d = bytes.TrimSpace(d)
	in := make([]map[string]string, 0)
	for l := range bytes.Lines(d) {
		x := make(map[string]string)
		if err := json.Unmarshal(l, &x); err != nil {
			return nil, fmt.Errorf("line %s: %w", string(l), err)
		}
		in = append(in, x)
	}

	k := &kribNotes{
		categories: categories,
	}
	for _, c := range k.categories {
		c.binds = make(map[string][]*bind, 0)
	}

	other := make(map[string][]*bind)
	for _, x := range in {
		switch {
		case x["kitty_mod"] != "":
			k.kmod = strings.Split(x["kitty_mod"], "+")
		default:
			b := &bind{
				mode:   x["mode"],
				keys:   strings.Split(x["keys"], "+"),
				action: x["action"],
			}

			var matched bool
			for _, c := range k.categories {
				if c.match(b) {
					matched = true
					c.binds[b.action] = append(c.binds[b.action], b)
				}
			}
			if !matched {
				other[b.action] = append(other[b.action], b)
			}
		}
	}

	for _, c := range k.categories {
		if c.name == "other" {
			c.binds = other
		}
	}

	return k, nil
}
