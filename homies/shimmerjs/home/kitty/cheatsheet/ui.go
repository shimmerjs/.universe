package main

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
)

var (
	titleBackground = lipgloss.Color("#5d6b66")
	titleForeground = lipgloss.Color("#9da9a0")

	keysColor   = lipgloss.Color("#d3c6aa")
	actionColor = lipgloss.Color("#d699b6")
	headerColor = lipgloss.Color("#5c3f4f")
	rowSepColor = lipgloss.Color("#859289")

	categoryWidth = 60

	docStyle = lipgloss.NewStyle().Padding(0, 1)

	titleContentStyle = lipgloss.NewStyle().
				Background(titleBackground).
				Foreground(titleForeground)

	categoryStyle = lipgloss.NewStyle().
			Margin(2, 1)
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Align(lipgloss.Center).
			Padding(1, 1).
			MarginBottom(1).
			Background(headerColor).
			Width(categoryWidth)
	tableStyle = lipgloss.NewStyle().
			Margin(1, 2)
	tableBorder = lipgloss.NewStyle().
			Foreground(rowSepColor).
			Faint(true)

	baseTable = func() *table.Table {
		return table.New()
	}

	// category header
)

func (k *kribNotes) Init() tea.Cmd { return nil }

func (k *kribNotes) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch keypress := msg.String(); keypress {
		case "q", "ctrl+c":
			return k, tea.Quit
		case "esc":
			if k.filter != "" {
				k.filter = ""
				k.viewport.SetContent(k.render())
				k.viewport.GotoTop()
			} else {
				return k, tea.Quit
			}
		default:
			for _, c := range k.categories {
				if keypress != c.key {
					continue
				}
				if k.filter == c.name {
					k.filter = ""
				} else {
					k.filter = c.name
				}
				k.viewport.SetContent(k.render())
				k.viewport.GotoTop()
				break
			}
		}

	case tea.WindowSizeMsg:
		k.width = msg.Width
		headerHeight := lipgloss.Height(k.renderHeader()) + 1 // +1 for the newline
		if !k.ready {
			k.viewport = viewport.New(
				viewport.WithWidth(msg.Width),
				viewport.WithHeight(msg.Height-headerHeight),
			)
			k.viewport.SetContent(k.render())
			k.ready = true
		} else {
			k.viewport.SetWidth(msg.Width)
			k.viewport.SetHeight(msg.Height - headerHeight)
			k.viewport.SetContent(k.render())
		}
	}

	k.viewport, cmd = k.viewport.Update(msg)
	return k, cmd
}

func (k *kribNotes) View() tea.View {
	var v tea.View
	v.AltScreen = true                    // use the full size of the terminal in its "alternate screen buffer"
	v.MouseMode = tea.MouseModeCellMotion // turn on mouse support so we can track the mouse wheel
	if !k.ready {
		v.SetContent("loading...")
		return v
	}
	v.SetContent(fmt.Sprintf("%s\n%s", k.renderHeader(), k.viewport.View()))
	return v
}

func (k *kribNotes) renderHeader() string {
	barMarginX := 2
	bar := titleContentStyle.
		Padding(0, 2).
		Margin(0, barMarginX).
		Width(k.width - barMarginX*2)

	title := bar.Render(titleContentStyle.
		Bold(true).
		Render("kitty kribsheet"),
	)

	activeColor := lipgloss.Color("#e6e6e6")
	inactiveColor := titleForeground
	keyColor := lipgloss.Color("#a7c080")

	nav := strings.Builder{}
	for _, c := range k.categories {
		if c.header {
			continue
		}
		keyStyle := lipgloss.NewStyle()
		nameStyle := lipgloss.NewStyle()

		switch {
		case k.filter == c.name:
			keyStyle = keyStyle.Foreground(activeColor).Bold(true)
			nameStyle = nameStyle.Foreground(activeColor).Bold(true)
		case k.filter != "":
			keyStyle = keyStyle.Foreground(inactiveColor).Faint(true)
			nameStyle = nameStyle.Foreground(inactiveColor).Faint(true)
		default:
			keyStyle = keyStyle.Foreground(keyColor).Bold(true)
			nameStyle = nameStyle.Foreground(inactiveColor)
		}

		nav.WriteString(" ")
		nav.WriteString(keyStyle.Render(c.key))
		nav.WriteString(nameStyle.Render(" " + c.name))
		nav.WriteString(" ")
	}

	navLine := lipgloss.NewStyle().
		Padding(0, 2).
		Margin(0, barMarginX).
		Render(nav.String())

	header := lipgloss.JoinVertical(lipgloss.Left, title, navLine)

	var subHeader []string

	if len(k.kmod) > 0 {
		kmodBox := lipgloss.NewStyle().
			Padding(1, 2).
			Width(categoryWidth).
			Render("kitty_mod = " + formatKeys(k.kmod))
		subHeader = append(subHeader, categoryStyle.Render(kmodBox))
	}

	for _, c := range k.categories {
		if !c.header || len(c.binds) == 0 {
			continue
		}
		subHeader = append(subHeader, categoryStyle.Render(
			mkCategoryTable().Rows(c.rows()...).Render(),
		))
	}

	if len(subHeader) > 0 {
		header = lipgloss.JoinVertical(lipgloss.Left, header, docStyle.Render(k.layoutColumns(subHeader)))
	}

	return header
}

func (k *kribNotes) colsPerRow() int {
	// categoryStyle has Margin(2, 1) = 1 char each side horizontally
	// docStyle has Padding(0, 1) = 1 char each side
	colWidth := categoryWidth + 2 // category + horizontal margin
	docPad := 2                   // docStyle horizontal padding
	return max(1, (k.width-docPad)/colWidth)
}

func (k *kribNotes) layoutColumns(items []string) string {
	if len(items) == 0 {
		return ""
	}
	numCols := min(k.colsPerRow(), len(items))
	if numCols <= 1 {
		return lipgloss.JoinVertical(lipgloss.Left, items...)
	}
	columns := make([][]string, numCols)
	colHeights := make([]int, numCols)
	for _, item := range items {
		shortest := 0
		for j := 1; j < numCols; j++ {
			if colHeights[j] < colHeights[shortest] {
				shortest = j
			}
		}
		columns[shortest] = append(columns[shortest], item)
		colHeights[shortest] += lipgloss.Height(item)
	}
	cols := make([]string, numCols)
	for i, col := range columns {
		cols[i] = lipgloss.JoinVertical(lipgloss.Left, col...)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cols...)
}

func (k *kribNotes) render() string {
	// Single category filter mode
	if k.filter != "" {
		c := k.getCategory(k.filter)
		if c == nil || len(c.binds) == 0 {
			return docStyle.Render("(no bindings)")
		}
		return docStyle.Render(k.renderCategory(c.name))
	}

	// Collect categories that have binds
	var items []string
	for _, c := range k.categories {
		if c.header || len(c.binds) == 0 {
			continue
		}
		items = append(items, k.renderCategory(c.name))
	}

	doc := k.layoutColumns(items)

	return docStyle.Render(doc)
}

func mkCategoryTable() *table.Table {
	return baseTable().
		BaseStyle(tableStyle).
		BorderStyle(tableBorder).
		BorderColumn(false).
		BorderRow(true).
		BorderRight(false).
		BorderLeft(false).
		BorderBottom(false).
		BorderTop(false).
		Width(categoryWidth).
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().
				Bold(true).
				MarginLeft(1)

			switch col {
			case 0:
				s = s.Foreground(keysColor).
					Width(25)
			case 1:
				s = s.Foreground(actionColor).MarginRight(1)
			default:
			}
			return s
		})
}

func (k *kribNotes) renderCategory(n string) string {
	c := lipgloss.JoinVertical(
		lipgloss.Left,
		headerStyle.Render(n),
		mkCategoryTable().Rows(k.getCategory(n).rows()...).Render(),
	)
	return categoryStyle.Render(c)
}
