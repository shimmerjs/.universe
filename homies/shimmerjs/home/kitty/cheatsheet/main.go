package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	// "github.com/charmbracelet/lipgloss"
)

// todo: add ordering for specific actions
// todo: parse out action aliases from config
// todo: accept config file
// todo: highlights/common section with specific commands featured
// todo: handle non-default modes?

var categories = map[string]*category{
	"scrolling": {
		selector: &categorySelector{
			re: "^scroll_|show_scrollback|show_.*_command_output|clear_terminal",
		},
	},
	"cliipboard": {
		selector: &categorySelector{
			re: "clipboard",
			actions: []string{
				"copy_or_noop",
				"paste_from_selction",
				"pass_selection_from_program",
			},
		},
	},
	"windows": {
		selector: &categorySelector{
			re: "window",
		},
	},
	"tabs": {
		selector: &categorySelector{
			re: "tab",
		},
	},
	"layout": {
		selector: &categorySelector{
			re: "layout",
		},
	},
	"system": {
		selector: &categorySelector{
			re:      "config|macos",
			actions: []string{"quit"},
		},
	},
}

type kribNotes struct {
	// all (via stdin)
	// user-configured (read file, hardcoded)
	kmod          []string
	categories    map[string]*category
	uncategorized map[string][]*bind
}

type category struct {
	idx      int // Priority relative to other cateogires
	selector *categorySelector
	binds    map[string][]*bind // can have multiple bindings per action
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

	for _, a := range s.actions {
		if a == b.action {
			return true
		}
	}

	return false
}

type bind struct {
	mode   string
	keys   []string
	action string
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

	fmt.Println(k.render())
	return nil
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
		fmt.Println("---")
		fmt.Println(string(l))
		fmt.Println("---")

		x := make(map[string]string)
		if err := json.Unmarshal(l, &x); err != nil {
			return nil, fmt.Errorf("line %s: %w", string(l), err)
		}
		in = append(in, x)
	}

	k := &kribNotes{
		categories:    categories,
		uncategorized: make(map[string][]*bind),
	}

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
				if c.selector.match(b) {
					matched = true
					if c.binds == nil {
						c.binds = make(map[string][]*bind)
					}
					c.binds[b.action] = append(c.binds[b.action], b)
				}
			}
			if !matched {
				k.uncategorized[b.action] = append(k.uncategorized[b.action], b)
			}
		}
	}
	// look for record that only has kitty mod set, pull mode/keys/action out of rest
	// to populate bindings and categorize them
	// also group by action to dedupe

	lines := strings.Split(string(d), "\n")
	fmt.Println(lines)

	return k, nil
}

func (k *kribNotes) render() string {
	doc := strings.Builder{}

	tw := tabwriter.NewWriter(&doc, 4, 0, 2, ' ', 0)

	for n, c := range k.categories {
		fmt.Fprintln(tw, "")
		fmt.Fprintln(tw, n)
		fmt.Fprintln(tw, "---")

		for a, binds := range c.binds {
			var b *bind
			b, binds = binds[0], binds[1:]
			fmt.Fprintf(tw, "%s\t%s\t\n",
				strings.Join(b.keys, "+"), a,
			)
			for _, x := range binds {
				fmt.Fprintf(tw, "%s\t\t\n", strings.Join(x.keys, "+"))
			}
		}
	}

	if len(k.uncategorized) > 0 {
		fmt.Fprintln(tw, "")
		fmt.Fprintln(tw, "misc")
		fmt.Fprintln(tw, "---")
		for a, binds := range k.uncategorized {
			var b *bind
			b, binds = binds[0], binds[1:]
			fmt.Fprintf(tw, "%s\t%s\t\n",
				strings.Join(b.keys, "+"), a,
			)
			for _, x := range binds {
				fmt.Fprintf(tw, "%s\t\t\n", strings.Join(x.keys, "+"))
			}
		}
	}

	tw.Flush()

	return doc.String()
}
