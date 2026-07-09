// Package pager is the height-driven column packer shared by the keyboard
// and touch shells: groups fill columns top to bottom in declared order,
// split across columns/pages when they do not fit, and every entry gets a
// cell rect for hit-testing. Layout is a pure function of its inputs.
package pager

import "fmt"

// Rect is a cell-grid rectangle: X,Y top-left origin, W,H in cells.
type Rect struct {
	X, Y, W, H int
}

// Contains reports whether the cell (x, y) falls inside r.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// Cell places one entry on a page.
type Cell struct {
	EntryID string
	Rect    Rect
}

// Header places a group header; Cont marks a continuation after a split.
type Header struct {
	Group string
	Rect  Rect
	Cont  bool
}

// Page is one screenful of placed headers and entry cells.
type Page struct {
	Headers []Header
	Cells   []Cell
}

// Group is an ordered slice of entry ids under one name. Order in and
// between groups is preserved verbatim; sorting happens upstream (classify).
type Group struct {
	Name string
	IDs  []string
}

// Params fixes the geometry for one layout run.
type Params struct {
	Width, Height int // page size in cells
	RowPitch      int // rows per entry: 1-2 keyboard, >=3 touch
	ColWidth      int // column width in cells
	GutterX       int // blank cells between columns
	HeaderRows    int // rows a group header consumes; 0 = no header rects
	GroupGap      int // blank rows after a group within a column
}

func (p Params) validate() error {
	switch {
	case p.RowPitch < 1:
		return fmt.Errorf("pager: rowPitch %d < 1", p.RowPitch)
	case p.ColWidth < 1:
		return fmt.Errorf("pager: colWidth %d < 1", p.ColWidth)
	case p.GutterX < 0 || p.HeaderRows < 0 || p.GroupGap < 0:
		return fmt.Errorf("pager: negative geometry")
	case p.Width < p.ColWidth:
		return fmt.Errorf("pager: width %d < colWidth %d", p.Width, p.ColWidth)
	case p.Height < p.HeaderRows+p.RowPitch:
		return fmt.Errorf("pager: height %d cannot fit a header plus one row", p.Height)
	}
	return nil
}

func (p Params) cols() int {
	return 1 + (p.Width-p.ColWidth)/(p.ColWidth+p.GutterX)
}

type cursor struct {
	pages []Page
	col   int
	y     int
	p     Params
}

func (c *cursor) page() *Page {
	if len(c.pages) == 0 {
		c.pages = append(c.pages, Page{})
	}
	return &c.pages[len(c.pages)-1]
}

func (c *cursor) x() int {
	return c.col * (c.p.ColWidth + c.p.GutterX)
}

// fits reports whether rows more rows fit in the current column.
func (c *cursor) fits(rows int) bool {
	return c.y+rows <= c.p.Height
}

func (c *cursor) advance() {
	c.y = 0
	c.col++
	if c.col >= c.p.cols() {
		c.col = 0
		c.pages = append(c.pages, Page{})
	}
}

// Layout packs groups into pages. Empty groups are skipped.
func Layout(groups []Group, p Params) ([]Page, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	c := &cursor{p: p}
	c.page() // ensure at least one page even with no input

	for _, g := range groups {
		if len(g.IDs) == 0 {
			continue
		}
		placed := false
		for _, id := range g.IDs {
			// a (continuation) header must fit together with the next row
			if !c.fits(p.HeaderRows+p.RowPitch) && !(placed && c.fits(p.RowPitch)) {
				c.advance()
			}
			if c.y == 0 || !placed {
				if p.HeaderRows > 0 {
					c.page().Headers = append(c.page().Headers, Header{
						Group: g.Name,
						Rect:  Rect{X: c.x(), Y: c.y, W: p.ColWidth, H: p.HeaderRows},
						Cont:  placed,
					})
					c.y += p.HeaderRows
				}
				placed = true
			}
			c.page().Cells = append(c.page().Cells, Cell{
				EntryID: id,
				Rect:    Rect{X: c.x(), Y: c.y, W: p.ColWidth, H: p.RowPitch},
			})
			c.y += p.RowPitch
		}
		c.y += p.GroupGap
	}
	return c.pages, nil
}
