package main

import (
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/shimmerjs/kittykrib/chord"
	"github.com/shimmerjs/kittykrib/classify"
	"github.com/shimmerjs/kittykrib/envelope"
)

// everforest palette, matching the old cheatsheet ui.go.
var (
	keysColor   = lipgloss.Color("#d3c6aa")
	cmdColor    = lipgloss.Color("#d699b6")
	headerColor = lipgloss.Color("#5c3f4f")
	rowSepColor = lipgloss.Color("#859289")
	dimColor    = lipgloss.Color("#9da9a0")

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Align(lipgloss.Center).
			Padding(0, 1).
			Background(headerColor)
	dimStyle = lipgloss.NewStyle().Foreground(dimColor)
	tableSep = lipgloss.NewStyle().Foreground(rowSepColor).Faint(true)
)

// render emits the whole sheet as static ANSI: linear sections, no cursor or
// screen-mode control sequences, safe for `| less -R`.
func render(env *envelope.Envelope, groups []classify.Grouped, width int) string {
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
		b.WriteString(renderEntries(env.Kind, g.Entries, width))
		b.WriteString("\n\n")
	}
	return b.String()
}

func renderEntries(kind string, entries []envelope.Entry, width int) string {
	rows := make([][]string, len(entries))
	for i, en := range entries {
		if kind == envelope.KindBindings {
			rows[i] = []string{chord.FormatSeq(en.Keys), en.Cmd}
		} else {
			rows[i] = []string{en.Term, en.Body}
		}
	}
	leftWidth := 26
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
				return s.Bold(true).Foreground(keysColor).Width(leftWidth)
			}
			return s.Foreground(cmdColor).MarginRight(1)
		}).
		Rows(rows...).
		Render()
}
