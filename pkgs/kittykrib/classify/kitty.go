package classify

// KittySheet is the Go port of the hardcoded category table from the old
// cheatsheet main.go. It seeds classification for kitty bindings envelopes
// until sheets/kitty.cue lands; the fixture parity test pins its behavior to
// the old table.
func KittySheet() Sheet {
	return Sheet{
		Name: "kitty",
		Sort: DefaultSort,
		Groups: []GroupSpec{
			{
				Name: "windows",
				Key:  "w",
				Match: &Match{
					Re: "window|layout_action",
					// splits-layout launches double as window management
					Exact: []string{
						"launch --location=hsplit --cwd=current",
						"launch --location=vsplit --cwd=current",
						"launch --location=split --cwd=current",
					},
				},
				Sort: append(append([]string{}, DefaultSort...), "int-keys-last"),
			},
			{Name: "tabs", Key: "t", Match: &Match{Re: "tab"}},
			{Name: "layout", Key: "l", Match: &Match{Re: "layout"}},
			{Name: "scrolling", Key: "m", Match: &Match{Re: "^scroll_|show_scrollback|show_.*_command_output|clear_terminal"}},
			{
				Name:   "system",
				Key:    "s",
				Header: true,
				Match:  &Match{Re: "config|macos|show_kitty_doc", Exact: []string{"quit"}},
			},
			{
				Name: "clipboard",
				Key:  "c",
				Match: &Match{
					Re: "clipboard|copy",
					Exact: []string{
						"copy_or_noop",
						"paste_from_selection",
						"pass_selection_from_program",
					},
				},
			},
			{Name: "other", Key: "o"},
		},
	}
}
