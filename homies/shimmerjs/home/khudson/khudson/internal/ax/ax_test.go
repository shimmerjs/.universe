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

const lsappinfoBundles = `3 app(s):
 1) "loginwindow" ASN:0x0-0x3003:
    bundleID="com.apple.loginwindow"
    pid = 446 type="UIElement" flavor=3
 2) [ 0x0-0x1a01a] "Google Chrome" ASN:0x0-0x1a01a:
    bundleID="com.google.Chrome"
    pid = 741 type="Foreground" flavor=3
 3) [ 0x0-0x2c02c] "Google Chrome" ASN:0x0-0x2c02c:
    bundleID="com.google.Chrome"
    pid = 902 type="Foreground" flavor=3
`

// Candidate pids come from every entry whose bundleID is EXACTLY the id --
// multiple instances yield multiple pids; prefixes, absent ids, and the
// empty id yield none.
func TestParseBundlePids(t *testing.T) {
	if got, want := parseBundlePids(lsappinfoBundles, "com.google.Chrome"), []int{741, 902}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseBundlePids(com.google.Chrome) = %v, want %v", got, want)
	}
	if got, want := parseBundlePids(lsappinfoBundles, "com.apple.loginwindow"), []int{446}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseBundlePids(com.apple.loginwindow) = %v, want %v", got, want)
	}
	if got := parseBundlePids(lsappinfoBundles, "com.google"); len(got) != 0 {
		t.Fatalf("parseBundlePids(com.google) = %v, want none (no prefix match)", got)
	}
	if got := parseBundlePids(lsappinfoBundles, "com.absent.app"); len(got) != 0 {
		t.Fatalf("parseBundlePids(com.absent.app) = %v, want none", got)
	}
	if got := parseBundlePids(lsappinfoBundles, ""); len(got) != 0 {
		t.Fatalf("parseBundlePids(\"\") = %v, want none", got)
	}
}

// axQuitSeams swaps the quit/force-quit exec seams for the test: listings
// pop off outs in order (repeating the last), kills and quits record.
func axQuitSeams(t *testing.T, outs ...string) (killed *[]int, quits *[]string) {
	t.Helper()
	oldList, oldKill, oldQuit := lsList, killPid, osaQuit
	t.Cleanup(func() { lsList, killPid, osaQuit = oldList, oldKill, oldQuit })
	killed, quits = &[]int{}, &[]string{}
	calls := 0
	lsList = func() (string, error) {
		out := outs[min(calls, len(outs)-1)]
		calls++
		return out, nil
	}
	killPid = func(pid int) error {
		*killed = append(*killed, pid)
		return nil
	}
	osaQuit = func(id string) error {
		*quits = append(*quits, id)
		return nil
	}
	return killed, quits
}

// The re-validation failure paths: a bundle absent at exec time errors with
// NO kill and NO quit, and a bundle whose pids all moved on between the
// resolve and the re-validate listings errors with NO kill.
func TestForceQuitBundleRevalidationFailureNoKill(t *testing.T) {
	// absent from the very first listing: both verbs refuse
	killed, quits := axQuitSeams(t, lsappinfoBundles)
	if err := ForceQuitBundle("com.absent.app"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("force-quit of an absent bundle = %v, want ErrNotFound", err)
	}
	if err := QuitBundle("com.absent.app"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("quit of an absent bundle = %v, want ErrNotFound", err)
	}
	if len(*killed) != 0 || len(*quits) != 0 {
		t.Fatalf("killed %v quit %v, want neither", *killed, *quits)
	}

	// resolved, then gone by the re-validation listing (quit or pid reuse
	// under another bundle): error, NO kill
	drifted := `1 app(s):
 1) "Imposter" ASN:0x0-0x9009:
    bundleID="com.other.app"
    pid = 741 type="Foreground" flavor=3
`
	killed, _ = axQuitSeams(t, lsappinfoBundles, drifted)
	if err := ForceQuitBundle("com.google.Chrome"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("force-quit after drift = %v, want ErrNotFound", err)
	}
	if len(*killed) != 0 {
		t.Fatalf("killed %v after the bundle drifted away, want none", *killed)
	}
}

// The happy paths: force-quit kills exactly the re-validated pids (a pid
// that dropped out between the snapshots is skipped, the surviving one
// dies), and quit sends one bundle-targeted AppleEvent, never a kill.
func TestForceQuitBundleKillsRevalidatedPids(t *testing.T) {
	killed, quits := axQuitSeams(t, lsappinfoBundles)
	if err := ForceQuitBundle("com.google.Chrome"); err != nil {
		t.Fatalf("force-quit = %v", err)
	}
	if want := []int{741, 902}; !reflect.DeepEqual(*killed, want) {
		t.Fatalf("killed = %v, want %v", *killed, want)
	}
	if len(*quits) != 0 {
		t.Fatalf("force-quit sent a quit AppleEvent: %v", *quits)
	}

	// partial drift: 741 left the bundle between the snapshots; only 902 dies
	partial := `1 app(s):
 1) [ 0x0-0x2c02c] "Google Chrome" ASN:0x0-0x2c02c:
    bundleID="com.google.Chrome"
    pid = 902 type="Foreground" flavor=3
`
	killed, _ = axQuitSeams(t, lsappinfoBundles, partial)
	if err := ForceQuitBundle("com.google.Chrome"); err != nil {
		t.Fatalf("force-quit with partial drift = %v", err)
	}
	if want := []int{902}; !reflect.DeepEqual(*killed, want) {
		t.Fatalf("killed = %v, want only the re-validated pid %v", *killed, want)
	}

	killed, quits = axQuitSeams(t, lsappinfoBundles)
	if err := QuitBundle("com.google.Chrome"); err != nil {
		t.Fatalf("quit = %v", err)
	}
	if !reflect.DeepEqual(*quits, []string{"com.google.Chrome"}) || len(*killed) != 0 {
		t.Fatalf("quit sent %v killed %v, want one bundle-targeted quit and no kill", *quits, *killed)
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
