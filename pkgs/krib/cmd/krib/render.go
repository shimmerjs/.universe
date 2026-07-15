package main

import (
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/shimmerjs/krib/chord"
	"github.com/shimmerjs/krib/classify"
	"github.com/shimmerjs/krib/envelope"
)

// theme defaults: the everforest palette matching the old cheatsheet ui.go.
// A sheet's theme block overrides per field; absent fields keep these.
var defaultTheme = resolvedTheme{
	keys:      "#d3c6aa",
	cmd:       "#d699b6",
	header:    "#5c3f4f",
	rowSep:    "#859289",
	dim:       "#9da9a0",
	leftWidth: 26,
}

type resolvedTheme struct {
	keys, cmd, header, rowSep, dim string
	leftWidth                      int
}

func resolveTheme(t *classify.Theme) resolvedTheme {
	r := defaultTheme
	if t == nil {
		return r
	}
	if t.Keys != "" {
		r.keys = t.Keys
	}
	if t.Cmd != "" {
		r.cmd = t.Cmd
	}
	if t.Header != "" {
		r.header = t.Header
	}
	if t.RowSep != "" {
		r.rowSep = t.RowSep
	}
	if t.Dim != "" {
		r.dim = t.Dim
	}
	if t.LeftWidth > 0 {
		r.leftWidth = t.LeftWidth
	}
	return r
}

// render emits the whole sheet as static ANSI: linear sections, no cursor or
// screen-mode control sequences, safe for `| less -R`.
func render(env *envelope.Envelope, groups []classify.Grouped, width int, theme *classify.Theme) string {
	t := resolveTheme(theme)
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Align(lipgloss.Center).
		Padding(0, 1).
		Background(lipgloss.Color(t.header))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.dim))

	if width < 20 {
		width = 20
	}
	var b strings.Builder
	if mods, ok := env.Meta.ModAliases["kitty_mod"]; ok {
		b.WriteString(dimStyle.Render("kitty_mod = " + chord.FormatMods(mods)))
		b.WriteString("\n\n")
	}
	for _, g := range groups {
		if len(g.Entries) == 0 {
			continue
		}
		b.WriteString(headerStyle.Width(width).Render(g.Name))
		b.WriteString("\n")
		if g.Meta.Description != "" {
			b.WriteString(dimStyle.Render(g.Meta.Description))
			b.WriteString("\n")
		}
		if g.Meta.WhenToUse != "" {
			b.WriteString(dimStyle.Render("when: " + g.Meta.WhenToUse))
			b.WriteString("\n")
		}
		if len(g.Meta.Phases) > 0 {
			b.WriteString(dimStyle.Render("phases: " + strings.Join(g.Meta.Phases, " -> ")))
			b.WriteString("\n")
		}
		b.WriteString(renderEntries(env.Kind, g.Entries, width, t))
		b.WriteString("\n\n")
	}
	return b.String()
}

func renderEntries(kind string, entries []envelope.Entry, width int, t resolvedTheme) string {
	rows := make([][]string, len(entries))
	for i, en := range entries {
		if kind == envelope.KindBindings {
			rows[i] = []string{chord.FormatSeq(en.Keys), en.Cmd}
		} else {
			rows[i] = []string{en.Term, en.Body}
		}
	}
	tableSep := lipgloss.NewStyle().Foreground(lipgloss.Color(t.rowSep)).Faint(true)
	return table.New().
		BorderStyle(tableSep).
		BorderColumn(false).
		BorderRow(true).
		BorderRight(false).
		BorderLeft(false).
		BorderBottom(false).
		BorderTop(false).
		Width(width).
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().MarginLeft(1)
			if col == 0 {
				return s.Bold(true).Foreground(lipgloss.Color(t.keys)).Width(t.leftWidth)
			}
			return s.Foreground(lipgloss.Color(t.cmd)).MarginRight(1)
		}).
		Rows(rows...).
		Render()
}
