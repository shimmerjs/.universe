// kitty sheet: classification, theme, and exec config for kitty bindings
// envelopes. CUE is the authoring format; the committed kitty.json is the
// loadable artifact (regenerate with `cue export kitty.cue -o kitty.json`;
// the krib-sheets nix check pins them equal).

name: "kitty"
sort: ["leading-key", "longest-last", "group-next-prev"]

// today's accept behavior: the raw kitty action string handed to
// `kitten @ action` as one argument -- argv exec, no shell. The {window}
// element targets the palette overlay's parent window and is dropped when
// no target is given.
exec: {
	run: "run"
	argv: ["kitten", "@", "action", "--match=id:{window}", "{cmd}"]
}

// everforest palette + layout, matching the retired render.go constants.
theme: {
	keys:      "#d3c6aa"
	cmd:       "#d699b6"
	header:    "#5c3f4f"
	rowSep:    "#859289"
	dim:       "#9da9a0"
	leftWidth: 26
}

groups: [
	{
		name: "windows"
		key:  "w"
		match: {
			re: "window|layout_action"
			// splits-layout launches double as window management
			exact: [
				"launch --location=hsplit --cwd=current",
				"launch --location=vsplit --cwd=current",
				"launch --location=split --cwd=current",
			]
		}
		sort: ["leading-key", "longest-last", "group-next-prev", "int-keys-last"]
	},
	{name: "tabs", key: "t", match: {re: "tab"}},
	{name: "layout", key: "l", match: {re: "layout"}},
	{name: "scrolling", key: "m", match: {re: "^scroll_|show_scrollback|show_.*_command_output|clear_terminal"}},
	{
		name:   "system"
		key:    "s"
		header: true
		match: {
			re: "config|macos|show_kitty_doc"
			exact: ["quit"]
		}
	},
	{
		name: "clipboard"
		key:  "c"
		match: {
			re: "clipboard|copy"
			exact: [
				"copy_or_noop",
				"paste_from_selection",
				"pass_selection_from_program",
			]
		}
	},
	{name: "other", key: "o"},
]

// execute-safety: quit/close-class actions require a second confirmation.
entries: [
	{match: {re: "^quit$|^close_"}, confirm: true},
]
