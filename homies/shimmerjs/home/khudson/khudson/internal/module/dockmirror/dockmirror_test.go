package dockmirror

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/ax"
	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// fakeExe stands in for the os.Executable() the module resolves in New:
// render takes it as a parameter and stays pure.
const fakeExe = "/fake/khudson"

const dockPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>autohide</key>
	<true/>
	<key>persistent-apps</key>
	<array>
		<dict>
			<key>GUID</key>
			<integer>3079093301</integer>
			<key>tile-data</key>
			<dict>
				<key>bundle-identifier</key>
				<string>com.google.Chrome</string>
				<key>dock-extra</key>
				<false/>
				<key>file-data</key>
				<dict>
					<key>_CFURLString</key>
					<string>file:///Applications/Google%20Chrome.app/</string>
					<key>_CFURLStringType</key>
					<integer>15</integer>
				</dict>
				<key>file-label</key>
				<string>Google Chrome</string>
				<key>file-mod-date</key>
				<integer>3776081456</integer>
				<key>file-type</key>
				<integer>41</integer>
			</dict>
			<key>tile-type</key>
			<string>file-tile</string>
		</dict>
		<dict>
			<key>GUID</key>
			<integer>3079093302</integer>
			<key>tile-data</key>
			<dict>
				<key>bundle-identifier</key>
				<string>net.kovidgoyal.kitty</string>
				<key>file-data</key>
				<dict>
					<key>_CFURLString</key>
					<string>file:///Applications/kitty.app/</string>
					<key>_CFURLStringType</key>
					<integer>15</integer>
				</dict>
				<key>file-label</key>
				<string>kitty</string>
			</dict>
			<key>tile-type</key>
			<string>file-tile</string>
		</dict>
	</array>
	<key>persistent-others</key>
	<array>
		<dict>
			<key>GUID</key>
			<integer>3079093400</integer>
			<key>tile-data</key>
			<dict>
				<key>file-data</key>
				<dict>
					<key>_CFURLString</key>
					<string>file:///Users/me/Downloads/</string>
					<key>_CFURLStringType</key>
					<integer>15</integer>
				</dict>
				<key>file-label</key>
				<string>Downloads</string>
			</dict>
			<key>tile-type</key>
			<string>directory-tile</string>
		</dict>
	</array>
	<key>tilesize</key>
	<integer>48</integer>
</dict>
</plist>
`

const lsappinfoList = `34 app(s):
        1) [ 0x0-0x25025] "Finder" ASN:0x0-0x25025:
            executable=/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder
            pid = 500 type="Foreground" flavor=3 Version="15.0" fileType="APPL" creator="MACS"
        2) [ 0x0-0x1a01a] "Google Chrome" ASN:0x0-0x1a01a:
            executable=/Applications/Google Chrome.app/Contents/MacOS/Google Chrome
            pid = 741 type="Foreground" flavor=3 Version="126.0.6478.127"
        3) [ 0x0-0x2c02c] "kitty" ASN:0x0-0x2c02c:
            pid = 902 type="Foreground" flavor=3 Version="0.35.2"
`

func TestParsePinnedApps(t *testing.T) {
	got := parsePinnedApps(dockPlist)
	want := []string{"Google Chrome", "kitty"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePinnedApps = %v, want %v", got, want)
	}
}

func TestParsePinnedAppsEmpty(t *testing.T) {
	if got := parsePinnedApps("<dict><key>persistent-apps</key>\n<array/>\n</dict>"); len(got) != 0 {
		t.Fatalf("parsePinnedApps on empty array = %v, want none", got)
	}
}

func TestParseRunning(t *testing.T) {
	got := parseRunning(lsappinfoList)
	want := map[string]bool{"Finder": true, "Google Chrome": true, "kitty": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRunning = %v, want %v", got, want)
	}
}

func TestRenderRunningOnly(t *testing.T) {
	// running apps only: pinned-and-running in Dock order, then the rest
	// alphabetically; pinned-but-not-running (A) never appears
	d := render(fakeExe, []string{"A", "B"}, map[string]bool{"B": true, "Telegram": true, "Slack": true}, nil, nil)
	if d.Title != "dock" {
		t.Fatalf("title = %q", d.Title)
	}
	want := []module.Row{
		{Kind: module.RowText, Text: "B", Act: []string{"open", "-a", "B"}},
		{Kind: module.RowText, Text: "Slack", Act: []string{"open", "-a", "Slack"}},
		{Kind: module.RowText, Text: "Telegram", Act: []string{"open", "-a", "Telegram"}},
	}
	if !reflect.DeepEqual(d.Rows, want) {
		t.Fatalf("rows = %+v\nwant %+v", d.Rows, want)
	}
}

// No cap: every running app must reach the rail, which truncates loudly for
// itself; a module-side cap would skew the rail's overflow count.
func TestRenderEmitsEveryRunningApp(t *testing.T) {
	running := map[string]bool{}
	for i := range 25 {
		running[fmt.Sprintf("App %02d", i)] = true
	}
	d := render(fakeExe, nil, running, nil, nil)
	if len(d.Rows) != 25 {
		t.Fatalf("rows = %d, want 25 (uncapped)", len(d.Rows))
	}
	for i, r := range d.Rows {
		if len(r.Act) != 3 {
			t.Fatalf("row %d lost its act: %+v", i, r)
		}
	}
}

// Real output shapes end to end: every Foreground app from lsappinfo must
// appear, kitty included.
func TestRenderFromRealOutputShapes(t *testing.T) {
	d := render(fakeExe, parsePinnedApps(dockPlist), parseRunning(lsappinfoList), nil, nil)
	want := []module.Row{
		{Kind: module.RowText, Text: "Google Chrome", Act: []string{"open", "-a", "Google Chrome"}},
		{Kind: module.RowText, Text: "kitty", Act: []string{"open", "-a", "kitty"}},
		{Kind: module.RowText, Text: "Finder", Act: []string{"open", "-a", "Finder"}},
	}
	if !reflect.DeepEqual(d.Rows, want) {
		t.Fatalf("rows = %+v\nwant %+v", d.Rows, want)
	}
}

// The app association is a best-effort trailing separator parse: a hyphen
// without surrounding spaces never splits, the LAST separator wins, and a
// bare title falls back to itself.
func TestTitleApp(t *testing.T) {
	cases := []struct{ title, want string }{
		{"Inbox - Google Chrome", "Google Chrome"},
		{"a - b - Google Chrome", "Google Chrome"},
		{"Inbox" + emDashSep + "Mail", "Mail"},
		{"caffeinate -d", "caffeinate -d"},
		{"Keymapp", "Keymapp"},
		{"trailing - ", "trailing - "},
	}
	for _, c := range cases {
		if got := titleApp(c.title); got != c.want {
			t.Errorf("titleApp(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}

// The AX sweep runs at most once per cadence tick: between ticks the
// cached result (windows or error) is reused without an exec; the param
// overrides the default 30s.
func TestMinimizedCadence(t *testing.T) {
	calls := 0
	clock := time.Unix(1000, 0)
	m := New()
	m.now = func() time.Time { return clock }
	m.sample = func(context.Context) ([]minWin, error) {
		calls++
		return []minWin{{title: "w", app: "w"}}, nil
	}

	for range 3 {
		if _, err := m.minimizedCached(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (cached between ticks)", calls)
	}
	clock = clock.Add(29 * time.Second)
	if _, _ = m.minimizedCached(context.Background(), nil); calls != 1 {
		t.Fatalf("calls = %d, want 1 at 29s (default 30s floor)", calls)
	}
	clock = clock.Add(2 * time.Second)
	if _, _ = m.minimizedCached(context.Background(), nil); calls != 2 {
		t.Fatalf("calls = %d, want 2 past the tick", calls)
	}

	// param override tightens the cadence
	params := map[string]any{"minimizedEvery": "1s"}
	clock = clock.Add(time.Second)
	if _, _ = m.minimizedCached(context.Background(), params); calls != 3 {
		t.Fatalf("calls = %d, want 3 with 1s override", calls)
	}

	// a cached ERROR is reused between ticks too: no exec storm while the
	// degrade note shows
	m.sample = func(context.Context) ([]minWin, error) {
		calls++
		return nil, fmt.Errorf("osascript: denied")
	}
	clock = clock.Add(2 * time.Second)
	if _, err := m.minimizedCached(context.Background(), params); err == nil || calls != 4 {
		t.Fatalf("calls = %d err = %v, want fresh failing sweep", calls, err)
	}
	if _, err := m.minimizedCached(context.Background(), params); err == nil || calls != 4 {
		t.Fatalf("calls = %d err = %v, want the cached error without an exec", calls, err)
	}
}

// The exec pair (defaults export + lsappinfo) runs at most once per cadence
// tick: between ticks the cached lists are reused without an exec -- every
// lsappinfo exec floods the unified log with launchservicesd entitlement
// denials, so the pair must never run at the widget poll. The param
// overrides the default 30s.
func TestAppsCadence(t *testing.T) {
	calls := 0
	clock := time.Unix(1000, 0)
	m := New()
	m.now = func() time.Time { return clock }
	m.list = func(context.Context) ([]string, map[string]bool, error) {
		calls++
		return []string{"kitty"}, map[string]bool{"kitty": true}, nil
	}

	for range 3 {
		if _, _, err := m.appsCached(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (cached between ticks)", calls)
	}
	clock = clock.Add(29 * time.Second)
	if _, _, _ = m.appsCached(context.Background(), nil); calls != 1 {
		t.Fatalf("calls = %d, want 1 at 29s (default 30s floor)", calls)
	}
	clock = clock.Add(2 * time.Second)
	if _, _, _ = m.appsCached(context.Background(), nil); calls != 2 {
		t.Fatalf("calls = %d, want 2 past the tick", calls)
	}

	// param override tightens the cadence
	params := map[string]any{"appsEvery": "1s"}
	clock = clock.Add(time.Second)
	if _, _, _ = m.appsCached(context.Background(), params); calls != 3 {
		t.Fatalf("calls = %d, want 3 with 1s override", calls)
	}

	// a cached ERROR is reused between ticks too: a failing lsappinfo must
	// not re-open the per-tick exec flood
	m.list = func(context.Context) ([]string, map[string]bool, error) {
		calls++
		return nil, nil, fmt.Errorf("lsappinfo: exit status 1")
	}
	clock = clock.Add(2 * time.Second)
	if _, _, err := m.appsCached(context.Background(), params); err == nil || calls != 4 {
		t.Fatalf("calls = %d err = %v, want fresh failing listing", calls, err)
	}
	if _, _, err := m.appsCached(context.Background(), params); err == nil || calls != 4 {
		t.Fatalf("calls = %d err = %v, want the cached error without an exec", calls, err)
	}
}

// A listing that failed because the poll budget expired is NOT cached:
// appsLast stays unset and the very next call re-lists instead of pinning
// the timeout for a whole cadence tick.
func TestAppsTimeoutNotCached(t *testing.T) {
	calls := 0
	clock := time.Unix(1000, 0)
	m := New()
	m.now = func() time.Time { return clock }
	m.list = func(ctx context.Context) ([]string, map[string]bool, error) {
		calls++
		return nil, nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := m.appsCached(ctx, nil); err == nil {
		t.Fatal("canceled listing returned no error")
	}
	if !m.appsLast.IsZero() {
		t.Fatalf("canceled listing pinned the cache: appsLast = %v", m.appsLast)
	}
	m.list = func(context.Context) ([]string, map[string]bool, error) {
		calls++
		return nil, map[string]bool{"kitty": true}, nil
	}
	_, running, err := m.appsCached(context.Background(), nil)
	if err != nil || !running["kitty"] {
		t.Fatalf("re-list = %v, %v; want the fresh listing", running, err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (timeout not cached)", calls)
	}
}

// Poll end to end through the seams: repeated polls inside one cadence tick
// exec nothing new and keep rendering the cached lists; a cached listing
// error fails the poll without an exec.
func TestPollUsesCachedApps(t *testing.T) {
	listCalls := 0
	clock := time.Unix(1000, 0)
	m := New()
	m.exe = fakeExe
	m.now = func() time.Time { return clock }
	m.list = func(context.Context) ([]string, map[string]bool, error) {
		listCalls++
		return parsePinnedApps(dockPlist), parseRunning(lsappinfoList), nil
	}
	m.sample = func(context.Context) ([]minWin, error) { return nil, nil }

	var first module.Data
	for i := range 3 {
		d, err := m.Poll(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = d
		} else if !reflect.DeepEqual(d, first) {
			t.Fatalf("poll %d = %+v\nwant the cached render %+v", i, d, first)
		}
	}
	if listCalls != 1 {
		t.Fatalf("list calls = %d, want 1 across 3 polls", listCalls)
	}
	if len(first.Rows) != 3 {
		t.Fatalf("rows = %+v, want the 3 running apps", first.Rows)
	}

	m.list = func(context.Context) ([]string, map[string]bool, error) {
		listCalls++
		return nil, nil, fmt.Errorf("lsappinfo: exit status 1")
	}
	clock = clock.Add(31 * time.Second)
	if _, err := m.Poll(context.Background(), nil); err == nil || listCalls != 2 {
		t.Fatalf("calls = %d err = %v, want the fresh failure to fail the poll", listCalls, err)
	}
	if _, err := m.Poll(context.Background(), nil); err == nil || listCalls != 2 {
		t.Fatalf("calls = %d err = %v, want the cached error without an exec", listCalls, err)
	}
}

// Minimized windows follow the running rows as dim actionable rows: Text =
// window title (owning app when untitled), Key = owning app, Act is the
// per-window unminimize verb -- [exe, "ax", "unminimize", title], plus
// ["--app", app] only when titleApp found a real split.
func TestRenderMinimizedSection(t *testing.T) {
	d := render(fakeExe, nil, map[string]bool{"kitty": true}, []minWin{
		{title: "Inbox - Google Chrome", app: "Google Chrome"},
		{title: "Keymapp", app: "Keymapp"},
	}, nil)
	want := []module.Row{
		{Kind: module.RowText, Text: "kitty", Act: []string{"open", "-a", "kitty"}},
		{Kind: module.RowText, Text: "Inbox - Google Chrome", Key: "Google Chrome", Style: module.StyleDim,
			Act: []string{fakeExe, "ax", "unminimize", "Inbox - Google Chrome", "--app", "Google Chrome"}},
		{Kind: module.RowText, Text: "Keymapp", Key: "Keymapp", Style: module.StyleDim,
			Act: []string{fakeExe, "ax", "unminimize", "Keymapp"}},
	}
	if !reflect.DeepEqual(d.Rows, want) {
		t.Fatalf("rows = %+v\nwant %+v", d.Rows, want)
	}
}

// A failed AX sweep degrades to ONE dim note row without an Act -- the
// expected state until khudson holds the Accessibility grant -- and never
// fails the poll. Minimized rows that did render KEEP the per-window
// unminimize argv unconditionally: the degrade path is the verb failing
// loud, never a downgrade to app-activate.
func TestRenderMinimizedDegrade(t *testing.T) {
	d := render(fakeExe, nil, map[string]bool{"kitty": true}, []minWin{{title: "w", app: "w"}},
		fmt.Errorf("dock AX sweep: %w", ax.ErrUntrusted))
	if len(d.Rows) != 3 {
		t.Fatalf("rows = %d, want running row + note row + window row: %+v", len(d.Rows), d.Rows)
	}
	note := d.Rows[1]
	if note.Kind != module.RowText || note.Style != module.StyleDim ||
		note.Text != "minimized: grant accessibility to khudson" || len(note.Act) != 0 {
		t.Fatalf("note row = %+v, want dim act-less %q", note, "minimized: grant accessibility to khudson")
	}
	if win := d.Rows[2]; !reflect.DeepEqual(win.Act, []string{fakeExe, "ax", "unminimize", "w"}) {
		t.Fatalf("window row act = %v, want the unconditional per-window argv", win.Act)
	}
}

// A sweep failure that is NOT the missing grant renders its own error text:
// only ax.ErrUntrusted earns the "grant accessibility" note -- anything else
// as that note would be a false diagnosis.
func TestRenderMinimizedGenericErrorNote(t *testing.T) {
	d := render(fakeExe, nil, nil, nil, fmt.Errorf("dock AX sweep: %w", errors.New("kAXErrorCannotComplete")))
	if len(d.Rows) != 1 {
		t.Fatalf("rows = %+v, want the single note row", d.Rows)
	}
	note := d.Rows[0]
	want := "minimized: sweep failed: dock AX sweep: kAXErrorCannotComplete"
	if note.Text != want || note.Style != module.StyleDim || len(note.Act) != 0 {
		t.Fatalf("note row = %+v, want dim act-less %q", note, want)
	}
}

// A sample that failed because the poll budget expired is NOT cached: m.last
// stays unset and the very next call re-samples instead of pinning the
// timeout for a whole cadence tick.
func TestMinimizedTimeoutNotCached(t *testing.T) {
	calls := 0
	clock := time.Unix(1000, 0)
	m := New()
	m.now = func() time.Time { return clock }
	m.sample = func(ctx context.Context) ([]minWin, error) {
		calls++
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.minimizedCached(ctx, nil); err == nil {
		t.Fatal("canceled sample returned no error")
	}
	if !m.last.IsZero() {
		t.Fatalf("canceled sample pinned the cache: last = %v", m.last)
	}
	m.sample = func(context.Context) ([]minWin, error) {
		calls++
		return []minWin{{title: "w", app: "w"}}, nil
	}
	mins, err := m.minimizedCached(context.Background(), nil)
	if err != nil || len(mins) != 1 {
		t.Fatalf("re-sample = %v, %v; want the fresh sweep", mins, err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (timeout not cached)", calls)
	}
}

// The mirror must not list khudson itself: the HUD's rebranded bundle
// (org.khudson.hud, display name "khudson") is dropped from the running
// tier by bundle id -- name-match only for entries without one -- and a
// real app survives.
func TestParseRunningOmitsSelf(t *testing.T) {
	out := `3 app(s):
        1) [ 0x0-0x25025] "Finder" ASN:0x0-0x25025:
            bundleID="com.apple.finder"
            pid = 500 type="Foreground" flavor=3
        2) [ 0x0-0x2c02c] "khudson" ASN:0x0-0x2c02c:
            bundleID="org.khudson.hud"
            pid = 902 type="Foreground" flavor=3
        3) [ 0x0-0x2d02d] "khudson" ASN:0x0-0x2d02d:
            bundleID=[ NULL ]
            pid = 903 type="Foreground" flavor=3
`
	got := parseRunning(out)
	want := map[string]bool{"Finder": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRunning = %v, want %v (self omitted)", got, want)
	}
	// a bundle id that is NOT the HUD keeps a khudson-named app: the name
	// fallback fires only when the entry carries no bundle id
	out = `1 app(s):
        1) [ 0x0-0x1001] "khudson" ASN:0x0-0x1001:
            bundleID="com.example.khudson-imposter"
            pid = 1 type="Foreground" flavor=3
`
	if got := parseRunning(out); !got["khudson"] {
		t.Fatalf("parseRunning = %v, want the non-HUD bundle kept", got)
	}
}

// The minimized tier omits khudson's own windows too (AX carries only
// names); a real app's minimized window remains.
func TestRenderMinimizedOmitsSelf(t *testing.T) {
	d := render(fakeExe, nil, nil, []minWin{
		{title: "khudson", app: "khudson"},
		{title: "Keymapp", app: "Keymapp"},
	}, nil)
	want := []module.Row{
		{Kind: module.RowText, Text: "Keymapp", Key: "Keymapp", Style: module.StyleDim,
			Act: []string{fakeExe, "ax", "unminimize", "Keymapp"}},
	}
	if !reflect.DeepEqual(d.Rows, want) {
		t.Fatalf("rows = %+v\nwant %+v", d.Rows, want)
	}
}

// An untrusted sweep surfaces through the sample seam as the typed
// ax.ErrUntrusted (cached between ticks like any sweep error) and renders
// as the single dim grant-note row -- never a hard poll failure.
func TestMinimizedUntrustedNote(t *testing.T) {
	m := New()
	clock := time.Unix(1000, 0)
	m.now = func() time.Time { return clock }
	m.sample = func(context.Context) ([]minWin, error) {
		return nil, fmt.Errorf("dock AX sweep: %w", ax.ErrUntrusted)
	}
	mins, err := m.minimizedCached(context.Background(), nil)
	if mins != nil || !errors.Is(err, ax.ErrUntrusted) {
		t.Fatalf("sweep = %v, %v; want nil windows + ErrUntrusted", mins, err)
	}
	d := render(fakeExe, nil, nil, mins, err)
	if len(d.Rows) != 1 {
		t.Fatalf("rows = %+v, want the single note row", d.Rows)
	}
	note := d.Rows[0]
	if note.Text != "minimized: grant accessibility to khudson" ||
		note.Style != module.StyleDim || len(note.Act) != 0 {
		t.Fatalf("note row = %+v, want dim act-less grant note", note)
	}
}
