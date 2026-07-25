package logiretch

import (
	"strings"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

func testMod() *Mod {
	return &Mod{
		now:    func() time.Time { return time.Unix(1000, 0) },
		held:   map[string]bool{},
		lit:    map[string]time.Time{},
		sockUp: true,
	}
}

func stateLine() wireLine {
	return wireLine{
		Kind: "state", Conn: true, DevIdx: 0xFF, Features: 45,
		Settings: map[string]wSet{
			"dpi":        {Want: "1600", Readback: "1600"},
			"smartShift": {Want: "mode=2 threshold=20", Readback: "02 14 00"},
			"haptic":     {Want: "50", Error: "feature absent"},
		},
		Remaps:  []wRemap{{CID: "0x00C3", Action: "clear-divert", Flags: "none"}},
		Pending: "haptic",
	}
}

// rowByKey collects all rows of a kind; spanText joins a spans row.
func spanText(r module.Row) string {
	var b strings.Builder
	for _, s := range r.Spans {
		b.WriteString(s.Text)
	}
	return b.String()
}

func TestRenderStateAndArt(t *testing.T) {
	m := testMod()
	m.apply(stateLine())
	d := m.render()

	art := 0
	for _, r := range d.Rows {
		if r.Key == KeyArt {
			art++
		}
	}
	if art < 8 {
		t.Errorf("expected a multi-line mouse diagram, got %d art rows", art)
	}

	// mapping rows carry the device readbacks and the pending retry
	var mapText strings.Builder
	for _, r := range d.Rows {
		if r.Kind == module.RowKV {
			mapText.WriteString(r.Key + "=" + r.Value + ";")
		}
	}
	got := mapText.String()
	for _, want := range []string{"dpi=1600 (dev 1600)", "haptic=50 ! feature absent", "remap=0x00C3 clear-divert", "pending=haptic (retrying)"} {
		if !strings.Contains(got, want) {
			t.Errorf("map rows %q lack %q", got, want)
		}
	}
}

func TestArtLightsControls(t *testing.T) {
	m := testMod()
	m.apply(wireLine{Kind: "input", Pressed: []string{"left"}, Held: []string{"left"}})
	m.apply(wireLine{Kind: "input", Wheel: -2})

	// a held button and a wheel roll both light their spans
	var litLabels []string
	for _, line := range m.artLines() {
		for _, s := range line {
			if s.Style == module.StyleHighlight {
				litLabels = append(litLabels, s.Text)
			}
		}
	}
	joined := strings.Join(litLabels, ",")
	if !strings.Contains(joined, "L") {
		t.Errorf("left button not lit: %q", joined)
	}
	if !strings.Contains(joined, "^") {
		t.Errorf("wheel-up glyph not lit: %q", joined)
	}

	// the wheel highlight decays after litFor; the held button does not
	m.now = func() time.Time { return time.Unix(1000, 0).Add(litFor + time.Millisecond) }
	m.mu.Lock()
	m.expire()
	m.mu.Unlock()
	if _, on := m.lit["wheel"]; on {
		t.Error("wheel highlight survived expiry")
	}
	if !m.held["left"] || !m.isLit("left") {
		t.Error("held button decayed")
	}
}

func TestSockDownAndDisconnect(t *testing.T) {
	m := testMod()
	m.sockUp = false
	rows := m.mapRows()
	if len(rows) != 1 || rows[0].Style != module.StyleWarn || !strings.Contains(rows[0].Text, "sock down") {
		t.Errorf("sock-down rows = %+v", rows)
	}

	// sock-up but link down: dim disconnect line, no settings
	m.sockUp = true
	m.apply(wireLine{Kind: "state", Conn: false})
	rows = m.mapRows()
	if len(rows) != 1 || !strings.Contains(rows[0].Text, "disconnected") {
		t.Errorf("disconnect rows = %+v", rows)
	}

	// setSockUp(false) clears held state (no release will arrive)
	m.apply(wireLine{Kind: "input", Pressed: []string{"middle"}, Held: []string{"middle"}})
	m.setSockUp(false)
	if len(m.held) != 0 {
		t.Errorf("sock drop kept held buttons: %v", m.held)
	}
}

func TestStatusRowBattery(t *testing.T) {
	m := testMod()
	m.apply(wireLine{Kind: "battery", SoC: 82, Charging: true})
	got := spanText(m.statusRow())
	if !strings.Contains(got, "link up") || !strings.Contains(got, "bat 82%+") {
		t.Errorf("status row = %q", got)
	}
}

// The art block must be exactly rectangular. The top-down silhouette this
// replaced drifted a column between rows -- 12 cells on some, 13 on
// others -- which showed up on glass as a ragged right edge. Padding is
// what guarantees it, so this pins the guarantee rather than the drawing.
func TestArtRowsAreRectangular(t *testing.T) {
	m := testMod()
	for i, row := range m.artLines() {
		n := 0
		for _, s := range row {
			n += len([]rune(s.Text))
		}
		if n != artW {
			t.Errorf("art row %d is %d cells, want %d", i, n, artW)
		}
	}
}

// Every callout names a control the event stream can actually light: a
// label wired to a key nothing publishes is a dead legend.
func TestArtCalloutsLight(t *testing.T) {
	m := testMod()
	for _, key := range []string{"wheel", "left", "right", "middle", "thumb", "back", "forward", "gesture", "btn6", "motion"} {
		m.lit = map[string]time.Time{key: m.now()}
		rows := m.artLines()

		var found bool
		for _, row := range rows {
			for _, s := range row {
				if s.Style == module.StyleHighlight {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("lighting %q lit nothing in the art", key)
		}
	}
}
