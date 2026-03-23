package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// TODO: flat list mode for fzf friendlyness
// TODO: parse out action aliases from config
// todo: highlights/common section with specific commands featured
// TODO: handle non-default modes?

var categories = []*category{
	{
		name: "windows",
		key:  "w",
		selector: &categorySelector{
			re: "window|layout_action",
			actions: []string{
				// since I use the customizable split layout exclusively, these actions
				// are essentially window management action aliases
				// TODO: use aliases to make easier to regex?
				"launch --location=hsplit --cwd=current",
				"launch --location=vsplit --cwd=current",
				"launch --location=split --cwd=current",
			},
		},
		sort: sortIntKeysLast,
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
			re:      "config|macos|show_kitty_doc",
			actions: []string{"quit"},
		},
		header: true,
	},
	{
		name: "clipboard",
		key:  "c",
		selector: &categorySelector{
			re: "clipboard|copy",
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
	other := k.getCategory("other")
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
					c.addBind(b)
				}
			}
			if !matched {
				other.addBind(b)
			}
		}
	}

	return k, nil
}
