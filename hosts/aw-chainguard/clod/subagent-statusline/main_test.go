package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var now = time.Unix(1765000000, 0)

func mk(t *testing.T, j string) task {
	t.Helper()
	var tk task
	if err := json.Unmarshal([]byte(j), &tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func stripANSI(s string) string {
	var b strings.Builder
	inCSI := false
	for _, r := range s {
		switch {
		case inCSI:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inCSI = false
			}
		case r == '\x1b':
			inCSI = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Unknown width -> compact inline form, segments only when present.
func TestInlineSparse(t *testing.T) {
	tk := mk(t, `{"id":"x","status":"running","label":"ms-start","tokenCount":100}`)
	content, hide := render(tk, 0, now)
	if hide {
		t.Fatal("unexpectedly hidden")
	}
	got := stripANSI(content)
	glyph, _ := statusStyle("running")
	want := glyph + " ms-start"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The bash predecessor shifted fields left whenever one was absent (tab-IFS
// collapse), rendering token counts as labels. Sparse tasks must render
// correctly.
func TestSparseTaskNoFieldShift(t *testing.T) {
	tk := mk(t, `{"id":"x","status":"running","label":"ms-start","tokenCount":100}`)
	content, hide := render(tk, 120, now)
	if hide {
		t.Fatal("unexpectedly hidden")
	}
	got := stripANSI(content)
	if !strings.HasPrefix(got, "⟳ ms-start") {
		t.Fatalf("label not where it belongs: %q", got)
	}
	if w := visibleWidth(content); w != 120 {
		t.Fatalf("visible width %d, want exactly 120", w)
	}
}

// The durability property: every row renders to exactly `columns` visible
// characters regardless of which fields are present, so rows align across the
// panel with no cross-row coordination.
func TestRowsAlignAtFixedWidth(t *testing.T) {
	rows := []string{
		`{"id":"a","status":"running","type":"researcher","label":"research:web","tokenCount":45230,"startTime":1764999917,"tokenSamples":[0,5,12,30,18]}`,
		`{"id":"b","status":"running","label":"sparse","tokenCount":100}`,
		`{"id":"c","status":"completed","label":"done-no-tokens"}`,
		`{"id":"d","status":"failed","label":"failed-with-elapsed","startTime":1764999917}`,
	}
	for _, j := range rows {
		content, hide := render(mk(t, j), 120, now)
		if hide {
			t.Fatalf("row hidden: %s", j)
		}
		if w := visibleWidth(content); w != 120 {
			t.Errorf("visible width %d, want 120 for %s", w, j)
		}
	}
}

func TestStartTimeFormats(t *testing.T) {
	start := now.Unix() - 83
	for name, raw := range map[string]string{
		"epoch-seconds": `1764999917`,
		"epoch-millis":  `1764999917000`,
		"string-epoch":  `"1764999917"`,
		"rfc3339":       `"` + time.Unix(start, 0).UTC().Format(time.RFC3339) + `"`,
	} {
		tk := mk(t, `{"id":"x","status":"running","label":"l","tokenCount":1,"startTime":`+raw+`}`)
		content, _ := render(tk, 120, now)
		if !strings.Contains(stripANSI(content), "1:23") {
			t.Errorf("%s: elapsed 1:23 missing in %q", name, stripANSI(content))
		}
	}
}

func TestQueuedIdleHidden(t *testing.T) {
	tk := mk(t, `{"id":"q","status":"queued","label":"cold"}`)
	content, hide := render(tk, 120, now)
	if !hide || content != "" {
		t.Fatalf("idle queued task should hide, got %q", content)
	}
}

func TestQueuedWithTokensShown(t *testing.T) {
	tk := mk(t, `{"id":"q","status":"queued","label":"warm","tokenCount":1200}`)
	content, hide := render(tk, 120, now)
	if hide {
		t.Fatal("active queued task should show")
	}
	if !strings.Contains(stripANSI(content), "warm") {
		t.Fatalf("label missing in %q", stripANSI(content))
	}
}

func TestTruncatesToColumns(t *testing.T) {
	long := strings.Repeat("x", 150)
	tk := mk(t, `{"id":"d","status":"running","label":"`+long+`","tokenCount":5000,"tokenSamples":[1,2,3],"startTime":1764999917}`)
	content, _ := render(tk, 80, now)
	if w := visibleWidth(content); w != 80 {
		t.Fatalf("visible width %d, want exactly 80", w)
	}
	if !strings.Contains(content, "...") {
		t.Fatal("expected truncation ellipsis")
	}
}

func TestTypeTag(t *testing.T) {
	tk := mk(t, `{"id":"x","status":"running","type":"researcher","label":"research:web","tokenCount":1}`)
	content, _ := render(tk, 120, now)
	if !strings.Contains(stripANSI(content), "[researcher]") {
		t.Fatal("type tag missing")
	}
	tk = mk(t, `{"id":"x","status":"running","type":"Explore","label":"explore","tokenCount":1}`)
	content, _ = render(tk, 120, now)
	if strings.Contains(stripANSI(content), "[explore]") {
		t.Fatal("type tag should be suppressed when it echoes the label")
	}
}

func TestUnknownStatusGlyph(t *testing.T) {
	tk := mk(t, `{"id":"x","status":"killed","label":"m","tokenCount":1}`)
	content, _ := render(tk, 120, now)
	if !strings.HasPrefix(stripANSI(content), glyphMisc) {
		t.Fatalf("want bullet fallback, got %q", stripANSI(content))
	}
}

func TestFormatters(t *testing.T) {
	for in, want := range map[int64]string{42: "42s", 83: "1:23", 3725: "1:02:05", -5: "0s"} {
		if got := fdur(in); got != want {
			t.Errorf("fdur(%d) = %q, want %q", in, got, want)
		}
	}
}
