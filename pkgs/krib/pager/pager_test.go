package pager

import (
	"fmt"
	"reflect"
	"testing"
)

func ids(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return out
}

func TestSingleColumnFill(t *testing.T) {
	p := Params{Width: 30, Height: 10, RowPitch: 1, ColWidth: 30, HeaderRows: 1}
	pages, err := Layout([]Group{{Name: "g", IDs: ids("e", 3)}}, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 {
		t.Fatalf("pages = %d", len(pages))
	}
	pg := pages[0]
	if len(pg.Headers) != 1 || pg.Headers[0].Rect != (Rect{0, 0, 30, 1}) || pg.Headers[0].Cont {
		t.Fatalf("header = %+v", pg.Headers)
	}
	want := []Cell{
		{"e0", Rect{0, 1, 30, 1}},
		{"e1", Rect{0, 2, 30, 1}},
		{"e2", Rect{0, 3, 30, 1}},
	}
	if !reflect.DeepEqual(pg.Cells, want) {
		t.Fatalf("cells = %+v", pg.Cells)
	}
}

func TestGroupSplitAcrossColumns(t *testing.T) {
	// 2 columns of height 5, header 1 row, pitch 1: 4 entries per column.
	p := Params{Width: 21, Height: 5, RowPitch: 1, ColWidth: 10, GutterX: 1, HeaderRows: 1}
	pages, err := Layout([]Group{{Name: "big", IDs: ids("e", 6)}}, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 {
		t.Fatalf("pages = %d", len(pages))
	}
	pg := pages[0]
	if len(pg.Headers) != 2 {
		t.Fatalf("headers = %+v", pg.Headers)
	}
	if pg.Headers[0].Cont || !pg.Headers[1].Cont {
		t.Fatalf("continuation flags wrong: %+v", pg.Headers)
	}
	if pg.Headers[1].Rect.X != 11 || pg.Headers[1].Rect.Y != 0 {
		t.Fatalf("second column header at %+v", pg.Headers[1].Rect)
	}
	// e4 starts the second column
	if pg.Cells[4].EntryID != "e4" || pg.Cells[4].Rect != (Rect{11, 1, 10, 1}) {
		t.Fatalf("split cell = %+v", pg.Cells[4])
	}
}

func TestPaging(t *testing.T) {
	// 1 column, height 4, header 1, pitch 1: 3 entries per page.
	p := Params{Width: 10, Height: 4, RowPitch: 1, ColWidth: 10, HeaderRows: 1}
	pages, err := Layout([]Group{{Name: "g", IDs: ids("e", 7)}}, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 {
		t.Fatalf("pages = %d", len(pages))
	}
	if n := len(pages[0].Cells); n != 3 {
		t.Fatalf("page 0 cells = %d", n)
	}
	if pages[2].Cells[0].EntryID != "e6" {
		t.Fatalf("page 2 = %+v", pages[2].Cells)
	}
	for i, pg := range pages[1:] {
		if len(pg.Headers) != 1 || !pg.Headers[0].Cont {
			t.Fatalf("page %d header = %+v", i+1, pg.Headers)
		}
	}
}

func TestGroupNeverStartsAtColumnBottom(t *testing.T) {
	// group a fills rows 0-2 (header+2), gap 1 puts cursor at 4; header+row
	// for b does not fit in height 5, so b starts a fresh column.
	p := Params{Width: 21, Height: 5, RowPitch: 1, ColWidth: 10, GutterX: 1, HeaderRows: 1, GroupGap: 1}
	pages, err := Layout([]Group{
		{Name: "a", IDs: ids("a", 2)},
		{Name: "b", IDs: ids("b", 2)},
	}, p)
	if err != nil {
		t.Fatal(err)
	}
	pg := pages[0]
	if pg.Headers[1].Group != "b" || pg.Headers[1].Rect.X != 11 || pg.Headers[1].Cont {
		t.Fatalf("group b header = %+v", pg.Headers[1])
	}
}

func TestTouchPitch(t *testing.T) {
	// touch strip: pitch 3, height 10, header 1 -> 3 entries per column.
	p := Params{Width: 10, Height: 10, RowPitch: 3, ColWidth: 10, HeaderRows: 1}
	pages, err := Layout([]Group{{Name: "g", IDs: ids("e", 4)}}, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("pages = %d", len(pages))
	}
	c := pages[0].Cells[2]
	if c.Rect != (Rect{0, 7, 10, 3}) {
		t.Fatalf("third cell = %+v", c)
	}
	if !c.Rect.Contains(5, 9) || c.Rect.Contains(5, 10) {
		t.Fatal("hit-test wrong")
	}
}

func TestEmptyGroupsSkipped(t *testing.T) {
	p := Params{Width: 10, Height: 5, RowPitch: 1, ColWidth: 10, HeaderRows: 1}
	pages, err := Layout([]Group{{Name: "empty"}, {Name: "g", IDs: ids("e", 1)}}, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages[0].Headers) != 1 || pages[0].Headers[0].Group != "g" {
		t.Fatalf("headers = %+v", pages[0].Headers)
	}
}

func TestDeterminism(t *testing.T) {
	p := Params{Width: 25, Height: 8, RowPitch: 2, ColWidth: 12, GutterX: 1, HeaderRows: 1, GroupGap: 1}
	gs := []Group{{Name: "a", IDs: ids("a", 5)}, {Name: "b", IDs: ids("b", 3)}}
	p1, err := Layout(gs, p)
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := Layout(gs, p)
	if !reflect.DeepEqual(p1, p2) {
		t.Fatal("layout not deterministic")
	}
}

func TestParamValidation(t *testing.T) {
	bad := []Params{
		{Width: 10, Height: 5, RowPitch: 0, ColWidth: 10},
		{Width: 10, Height: 5, RowPitch: 1, ColWidth: 0},
		{Width: 5, Height: 5, RowPitch: 1, ColWidth: 10},
		{Width: 10, Height: 1, RowPitch: 1, ColWidth: 10, HeaderRows: 2},
		{Width: 10, Height: 5, RowPitch: 1, ColWidth: 10, GutterX: -1},
	}
	for i, p := range bad {
		if _, err := Layout(nil, p); err == nil {
			t.Errorf("case %d: want error", i)
		}
	}
}
