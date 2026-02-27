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
		Padding(1, 2).
		Margin(0, barMarginX).
		Width(k.width - barMarginX*2)

	activeColor := lipgloss.Color("#e6e6e6")

	h1 := strings.Builder{}

	h1.WriteString(titleContentStyle.
		Bold(true).
		Render("kitty kribsheet"),
	)

	for _, c := range k.categories {
		label := fmt.Sprintf(" (%s) %s", c.key, c.name)
		s := titleContentStyle.
			AlignHorizontal(lipgloss.Right)

		switch {
		case k.filter == c.name:
			s = s.
				Foreground(activeColor).
				Bold(true)
		case k.filter != "":
			s = s.
				Faint(true)
		}

		h1.WriteString(s.Render(label))
	}

	header := bar.Render(h1.String())

	if len(k.kmod) > 0 {
		kmodLine := lipgloss.NewStyle().
			Padding(1, 2).
			Margin(1, barMarginX, 0).
			Render("kitty_mod = " + formatKeys(k.kmod))
		header = lipgloss.JoinVertical(lipgloss.Left, header, kmodLine)
	}

	return header
}

func (k *kribNotes) colsPerRow() int {
	// categoryStyle has Margin(2, 1) = 1 char each side horizontally
	// docStyle has Padding(0, 1) = 1 char each side
	colWidth := categoryWidth + 2 // category + horizontal margin
	docPad := 2                   // docStyle horizontal padding
	n := (k.width - docPad) / colWidth
	if n < 1 {
		return 1
	}
	return n
}

func (k *kribNotes) render() string {
	// Single category filter mode
	if k.filter != "" {
		c := k.getCategory(k.filter)
		if c == nil || len(c.binds) == 0 {
			return docStyle.Render("(no bindings)")
		}
		if c.name == "other" {
			return docStyle.Render(k.renderOther())
		}
		return docStyle.Render(k.renderCategory(c.name))
	}

	// Collect named categories (everything except "other") that have binds
	var named []string
	for _, c := range k.categories {
		if c.name != "other" && len(c.binds) > 0 {
			named = append(named, c.name)
		}
	}

	cols := k.colsPerRow()

	// Render named categories in rows of `cols`
	var rows []string
	for i := 0; i < len(named); i += cols {
		end := i + cols
		if end > len(named) {
			end = len(named)
		}
		chunk := named[i:end]
		rendered := make([]string, len(chunk))
		for j, name := range chunk {
			rendered[j] = k.renderCategory(name)
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, rendered...))
	}

	doc := lipgloss.JoinVertical(lipgloss.Center, rows...)

	// "other" category
	other := k.renderOther()
	if other != "" {
		doc = lipgloss.JoinVertical(lipgloss.Center, doc, other)
	}

	return docStyle.Render(doc)
}

func (k *kribNotes) renderOther() string {
	uncategorizedRows := k.getCategory("other").binds.rows()
	if len(uncategorizedRows) == 0 {
		return ""
	}

	cols := k.colsPerRow()
	otherCols := cols
	if otherCols > len(uncategorizedRows) {
		otherCols = len(uncategorizedRows)
	}
	chunkSize := (len(uncategorizedRows) + otherCols - 1) / otherCols // ceiling division
	otherRendered := make([]string, 0, otherCols)
	for i := 0; i < len(uncategorizedRows); i += chunkSize {
		end := i + chunkSize
		if end > len(uncategorizedRows) {
			end = len(uncategorizedRows)
		}
		otherRendered = append(otherRendered, mkCategoryTable().Rows(uncategorizedRows[i:end]...).Render())
	}
	otherWidth := cols * categoryWidth
	if otherWidth > k.width {
		otherWidth = k.width
	}
	return categoryStyle.Render(
		lipgloss.JoinVertical(lipgloss.Center,
			headerStyle.Width(otherWidth).Render("other"),
			lipgloss.JoinHorizontal(lipgloss.Left, otherRendered...),
		),
	)
}

// table needs to be struct probably to track all the necessary state for
// conditional rendering

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
		mkCategoryTable().Rows(k.getCategory(n).binds.rows()...).Render(),
	)
	return categoryStyle.Render(c)
}
