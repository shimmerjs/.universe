package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// renoLSFixture builds an ls tree: windows[i] = {id, handle, focused}.
func renoLSFixture(wins ...[3]any) string {
	var b strings.Builder
	b.WriteString(`[{"id":1,"is_focused":true,"tabs":[{"id":1,"is_focused":true,"windows":[`)
	for i, w := range wins {
		if i > 0 {
			b.WriteString(",")
		}
		vars := "{}"
		if w[1].(string) != "" {
			vars = fmt.Sprintf(`{"reno_handle":%q}`, w[1])
		}
		fmt.Fprintf(&b, `{"id":%d,"title":"t%d","is_focused":%v,"user_vars":%s}`, w[0], w[0], w[2], vars)
	}
	b.WriteString(`]}]}]`)
	return b.String()
}

// renoTestVerbs wires a RenoVerbs against a scripted kitten: each call
// consumes the next canned response; every argv is recorded.
func renoTestVerbs(t *testing.T, responses ...string) (*RenoVerbs, *[]string) {
	t.Helper()
	dir := t.TempDir()
	calls := &[]string{}
	i := 0
	v := &RenoVerbs{
		Socket:     "sock",
		EventsPath: filepath.Join(dir, "events.jsonl"),
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			*calls = append(*calls, strings.Join(args, " "))
			if i >= len(responses) {
				t.Fatalf("kitten called %d times, scripted %d: %v", i+1, len(responses), *calls)
			}
			r := responses[i]
			i++
			if strings.HasPrefix(r, "ERR:") {
				return nil, fmt.Errorf("%s", r[4:])
			}
			return []byte(r), nil
		},
		openStart: func(context.Context, []string) error { return nil },
		now:       func() time.Time { return time.Date(2026, 7, 22, 15, 0, 0, 0, time.UTC) },
	}
	return v, calls
}

func readEvents(t *testing.T, v *RenoVerbs) []renoEvent {
	t.Helper()
	b, err := os.ReadFile(v.EventsPath)
	if err != nil {
		t.Fatalf("events journal: %v", err)
	}
	var out []renoEvent
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e renoEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

func TestFocusSessionConfirmed(t *testing.T) {
	corr := RenoCorr{Run: "r1", Step: 2, Act: 0}
	v, calls := renoTestVerbs(t,
		renoLSFixture([3]any{12, "work", false}),
		"", // focus-window
		renoLSFixture([3]any{12, "work", true}),
	)
	if err := v.FocusSession(context.Background(), "work", corr); err != nil {
		t.Fatalf("focus: %v", err)
	}
	if (*calls)[1] != "focus-window --match id:12" {
		t.Errorf("calls = %v", *calls)
	}
	ev := readEvents(t, v)
	if len(ev) != 1 || ev[0].Outcome != "ok" || ev[0].Run != "r1" || ev[0].Step != 2 {
		t.Errorf("events = %+v", ev)
	}
}

func TestFocusSessionMissAndAmbiguous(t *testing.T) {
	v, calls := renoTestVerbs(t, renoLSFixture([3]any{12, "", false}))
	err := v.FocusSession(context.Background(), "work", RenoCorr{Step: -1, Act: -1})
	if err == nil || !strings.Contains(err.Error(), "no window carries") {
		t.Fatalf("miss: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("miss must not act: %v", *calls)
	}
	if ev := readEvents(t, v); ev[0].Outcome != "miss" {
		t.Errorf("events = %+v", ev)
	}

	v2, calls2 := renoTestVerbs(t, renoLSFixture([3]any{12, "work", false}, [3]any{13, "work", false}))
	err = v2.FocusSession(context.Background(), "work", RenoCorr{})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "window 12") || !strings.Contains(err.Error(), "window 13") {
		t.Fatalf("ambiguous: %v", err)
	}
	if len(*calls2) != 1 {
		t.Fatalf("ambiguous must not act: %v", *calls2)
	}
}

func TestFocusSessionUnconfirmed(t *testing.T) {
	v, _ := renoTestVerbs(t,
		renoLSFixture([3]any{12, "work", false}),
		"",
		renoLSFixture([3]any{12, "work", false}), // still not focused
	)
	err := v.FocusSession(context.Background(), "work", RenoCorr{})
	if err == nil || !strings.Contains(err.Error(), "not focused") {
		t.Fatalf("unconfirmed: %v", err)
	}
	if ev := readEvents(t, v); ev[0].Outcome != "unconfirmed" {
		t.Errorf("events = %+v", ev)
	}
}

func TestLaunchSession(t *testing.T) {
	// ensure-semantics: one already-planted window is success (prev/goto
	// re-enter launch steps), never a mid-show failure
	v, calls := renoTestVerbs(t, renoLSFixture([3]any{12, "aux", false}))
	if err := v.LaunchSession(context.Background(), "aux", "", false, nil, RenoCorr{}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("ensure must not launch again: %v", *calls)
	}
	if ev := readEvents(t, v); ev[0].Outcome != "ok" || !strings.Contains(ev[0].Detail, "already planted") {
		t.Fatalf("ensure event = %+v", ev)
	}

	// two planted windows is the ambiguity every later focus trips on
	v1, calls1 := renoTestVerbs(t, renoLSFixture([3]any{12, "aux", false}, [3]any{13, "aux", false}))
	err := v1.LaunchSession(context.Background(), "aux", "", false, nil, RenoCorr{})
	if err == nil || !strings.Contains(err.Error(), "already planted 2 times") {
		t.Fatalf("dup: %v", err)
	}
	if len(*calls1) != 1 {
		t.Fatalf("dup must not launch: %v", *calls1)
	}

	// happy path: launch argv shape + post-ls confirmation
	v2, calls2 := renoTestVerbs(t,
		renoLSFixture([3]any{12, "", false}),
		"", // launch
		renoLSFixture([3]any{12, "", false}, [3]any{20, "aux", false}),
	)
	if err := v2.LaunchSession(context.Background(), "aux", "/tmp", true, []string{"claude"}, RenoCorr{Run: "r2"}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	want := "launch --type tab --var reno_handle=aux --cwd /tmp --keep-focus claude"
	if (*calls2)[1] != want {
		t.Errorf("launch argv = %q, want %q", (*calls2)[1], want)
	}
	ev := readEvents(t, v2)
	if ev[0].Outcome != "ok" || !strings.Contains(ev[0].Detail, "window 20") {
		t.Errorf("events = %+v", ev)
	}
}

func TestOpenURL(t *testing.T) {
	v, _ := renoTestVerbs(t)
	var opened []string
	v.openStart = func(_ context.Context, argv []string) error {
		opened = append(opened, strings.Join(argv, " "))
		return nil
	}
	if err := v.OpenURL(context.Background(), "https://x/y", RenoCorr{}); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != "open https://x/y" {
		t.Errorf("opened = %v", opened)
	}
	if ev := readEvents(t, v); ev[0].Outcome != "ok" {
		t.Errorf("events = %+v", ev)
	}
}

// A verb whose action lands but whose journal write fails must exit
// nonzero: the events journal backs wait guards and post-mortems, so a
// silent miss is worse than a dead step (deliberate delta from
// claudeverb's swallowed logf errors).
func TestJournalFailureFailsTheVerb(t *testing.T) {
	v, _ := renoTestVerbs(t)
	blocker := filepath.Join(t.TempDir(), "flat")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	v.EventsPath = filepath.Join(blocker, "events.jsonl") // parent is a file: writes fail
	err := v.OpenURL(context.Background(), "https://x", RenoCorr{})
	if err == nil || !strings.Contains(err.Error(), "events journal") {
		t.Fatalf("journal failure must fail the verb: %v", err)
	}
}
