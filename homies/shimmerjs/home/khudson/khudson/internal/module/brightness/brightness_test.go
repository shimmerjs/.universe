package brightness

import (
	"strings"
	"testing"
)

const listFixture = `[1] DELL U3223QE (37D8113D-6E52-4B3E-9A2C-1D0F8E7A6B5C)
[2] XENEON EDGE (5DDA2C4F-8A1B-4C2D-9E3F-0A1B2C3D4E5F)
`

func TestParseDisplayList(t *testing.T) {
	ds := parseDisplayList(listFixture)
	if len(ds) != 2 {
		t.Fatalf("got %d displays, want 2: %+v", len(ds), ds)
	}
	if ds[0].index != 1 || ds[0].name != "DELL U3223QE" {
		t.Errorf("ds[0] = %+v, want {1 DELL U3223QE}", ds[0])
	}
	if ds[1].index != 2 || ds[1].name != "XENEON EDGE" {
		t.Errorf("ds[1] = %+v, want {2 XENEON EDGE}", ds[1])
	}
}

func TestParseDisplayListEmptyAndGarbage(t *testing.T) {
	if ds := parseDisplayList(""); len(ds) != 0 {
		t.Errorf("empty input: got %+v", ds)
	}
	if ds := parseDisplayList("no displays found\n"); len(ds) != 0 {
		t.Errorf("garbage input: got %+v", ds)
	}
	if ds := parseDisplayList("[x] BROKEN (abc)\n"); len(ds) != 0 {
		t.Errorf("bad index: got %+v", ds)
	}
}

func TestDisplayMissing(t *testing.T) {
	ds := parseDisplayList(listFixture)
	want := "LG ULTRAFINE"
	found := false
	for _, d := range ds {
		if strings.Contains(strings.ToLower(d.name), strings.ToLower(want)) {
			found = true
		}
	}
	if found {
		t.Errorf("%q should not match fixture displays %+v", want, ds)
	}
}

func TestCaseInsensitiveSubstringMatch(t *testing.T) {
	ds := parseDisplayList(listFixture)
	want := "xeneon"
	var got display
	for _, d := range ds {
		if strings.Contains(strings.ToLower(d.name), strings.ToLower(want)) {
			got = d
			break
		}
	}
	if got.index != 2 {
		t.Errorf("match %q: got %+v, want index 2", want, got)
	}
}

func TestParseIntOutput(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"62\n", 62, true},
		{"  100  ", 100, true},
		{"0", 0, true},
		{"", 0, false},
		{"error: no DDC\n", 0, false},
	} {
		v, err := parseIntOutput(tc.in)
		if tc.ok && (err != nil || v != tc.want) {
			t.Errorf("parseIntOutput(%q) = %d, %v; want %d, nil", tc.in, v, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Errorf("parseIntOutput(%q) = %d, nil; want error", tc.in, v)
		}
	}
}
