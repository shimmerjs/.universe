package ax

import (
	"errors"
	"reflect"
	"testing"
)

// Pure-logic units only: no AX calls, so the suite passes inside
// buildGoModule's doCheck without an Accessibility grant or GUI session.
// The live walk/press paths are in ax_live_test.go behind KHUDSON_AX.

func TestParsePid(t *testing.T) {
	cases := []struct {
		out  string
		want int
		ok   bool
	}{
		{`"pid"=34400`, 34400, true},
		{`"pid" = 500` + "\n", 500, true},
		{"", 0, false},
		{`"pid"=`, 0, false},
		{`nothing here`, 0, false},
	}
	for _, c := range cases {
		got, err := parsePid(c.out)
		if c.ok != (err == nil) || got != c.want {
			t.Errorf("parsePid(%q) = %d, %v; want %d, ok=%v", c.out, got, err, c.want, c.ok)
		}
	}
}

const lsappinfoList = `34 app(s):
 1) "loginwindow" ASN:0x0-0x3003:
    bundleID="com.apple.loginwindow"
    pid = 446 type="UIElement" flavor=3 Version="3085.5.3" fileType="APPL" creator="lgnw" Arch=ARM64
 2) [ 0x0-0x1a01a] "Google Chrome" ASN:0x0-0x1a01a:
    executable=/Applications/Google Chrome.app/Contents/MacOS/Google Chrome
    pid = 741 type="Foreground" flavor=3 Version="126.0.6478.127"
 3) [ 0x0-0x2c02c] "Google Chrome" ASN:0x0-0x2c02c:
    pid = 902 type="Foreground" flavor=3
`

// Candidate pids come from every entry named EXACTLY app -- multiple
// instances yield multiple pids, substrings and absent names yield none.
func TestParseAppPids(t *testing.T) {
	if got, want := parseAppPids(lsappinfoList, "Google Chrome"), []int{741, 902}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseAppPids(Google Chrome) = %v, want %v", got, want)
	}
	if got := parseAppPids(lsappinfoList, "Chrome"); len(got) != 0 {
		t.Fatalf("parseAppPids(Chrome) = %v, want none (no substring match)", got)
	}
	if got := parseAppPids(lsappinfoList, "kitty"); len(got) != 0 {
		t.Fatalf("parseAppPids(kitty) = %v, want none", got)
	}
	if got, want := parseAppPids(lsappinfoList, "loginwindow"), []int{446}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseAppPids(loginwindow) = %v, want %v", got, want)
	}
}

// Matching is EXACT equality: case, whitespace, prefix, and separator
// near-misses must never press somebody else's window.
func TestTitleMatches(t *testing.T) {
	cases := []struct {
		got, want string
		match     bool
	}{
		{"Inbox", "Inbox", true},
		{"inbox", "Inbox", false},
		{"Inbox ", "Inbox", false},
		{" Inbox", "Inbox", false},
		{"Inbox - Google Chrome", "Inbox", false},
		{"", "", true},
	}
	for _, c := range cases {
		if titleMatches(c.got, c.want) != c.match {
			t.Errorf("titleMatches(%q, %q) = %v, want %v", c.got, c.want, !c.match, c.match)
		}
	}
}

// The error taxonomy: kAXErrorAPIDisabled is the untrusted state, other
// codes wrap as *AXError with the code preserved, and not-found wraps
// ErrNotFound -- all distinguishable via errors.Is/As.
func TestErrorTaxonomy(t *testing.T) {
	if err := mapAXError("press", axErrAPIDisabled); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("mapAXError(APIDisabled) = %v, want ErrUntrusted", err)
	}
	err := mapAXError("press", -25202)
	var axe *AXError
	if !errors.As(err, &axe) || axe.Code != -25202 || axe.Op != "press" {
		t.Fatalf("mapAXError(-25202) = %v, want *AXError carrying the code", err)
	}
	if errors.Is(err, ErrUntrusted) || errors.Is(err, ErrNotFound) {
		t.Fatalf("mapAXError(-25202) = %v, must not be untrusted or not-found", err)
	}
	nf := notFound(`dock item "x"`)
	if !errors.Is(nf, ErrNotFound) || errors.Is(nf, ErrUntrusted) {
		t.Fatalf("notFound = %v, want ErrNotFound only", nf)
	}
}
