package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func testModel() *model {
	return &model{
		held: map[string]bool{},
		lit:  map[string]time.Time{},
		w:    80, h: 20,
		now: time.Unix(100, 0),
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
		Remaps:   []wRemap{{CID: "0x00C3", Action: "clear-divert", Flags: "none"}},
		Takeover: "walked",
		Pending:  "haptic",
	}
}

func render(m *model) string {
	v := m.View()
	return v.Content
}

func TestMappingViewRendersState(t *testing.T) {
	m := testModel()
	m.apply(stateLine())
	out := render(m)
	for _, want := range []string{
		"dpi", "1600", "(dev: 1600)",
		"smartShift", "mode=2 threshold=20",
		"haptic", "feature absent",
		"remap", "0x00C3 clear-divert",
		"pending", "haptic (retrying)",
		"takeover", "walked",
		"thumbwheel", "(not configured)",
		"link: up",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("view lacks %q", want)
		}
	}
}

func TestInputLightsControls(t *testing.T) {
	m := testModel()
	m.apply(stateLine())
	m.apply(wireLine{Kind: "input", Pressed: []string{"left"}, Held: []string{"left"}})
	m.apply(wireLine{Kind: "input", Wheel: -3, DX: 5, DY: 0})
	if !m.held["left"] {
		t.Error("left not held")
	}
	for _, k := range []string{"left", "wheel", "motion"} {
		if _, on := m.lit[k]; !on {
			t.Errorf("%s not lit", k)
		}
	}
	out := render(m)
	if !strings.Contains(out, "wheel ^") {
		t.Errorf("wheel direction glyph missing:\n%s", out)
	}

	// release: highlight decays after litFor, held clears immediately
	m.apply(wireLine{Kind: "input", Released: []string{"left"}})
	if m.held["left"] {
		t.Error("release kept left held")
	}
	m.now = m.now.Add(litFor + time.Millisecond)
	m.expire()
	if _, on := m.lit["left"]; on {
		t.Error("highlight survived expiry")
	}
}

// A held button never decays: the highlight IS the held state.
func TestHeldButtonsStayLit(t *testing.T) {
	m := testModel()
	m.apply(wireLine{Kind: "input", Pressed: []string{"middle"}, Held: []string{"middle"}})
	m.now = m.now.Add(10 * litFor)
	m.expire()
	if _, on := m.lit["middle"]; !on {
		t.Error("held button decayed")
	}
}

func TestDisconnectedStates(t *testing.T) {
	m := testModel()
	out := render(m)
	if !strings.Contains(out, "no state line yet") || !strings.Contains(out, "logiretch.sock down") {
		t.Errorf("cold view = %q", out)
	}

	m.apply(stateLine())
	down := wireLine{Kind: "state"} // connected:false
	m.apply(down)
	out = render(m)
	if !strings.Contains(out, "mouse disconnected") {
		t.Errorf("link-down view lacks disconnect row")
	}

	// socket drop clears held state (no release will ever arrive)
	m.apply(wireLine{Kind: "input", Pressed: []string{"left"}, Held: []string{"left"}})
	m.apply(wireLine{Kind: sockDown})
	if len(m.held) != 0 {
		t.Error("sock-down kept held buttons")
	}
	if !strings.Contains(render(m), "logiretch.sock down") {
		t.Error("sock-down not surfaced")
	}
}

func TestBatteryRow(t *testing.T) {
	m := testModel()
	m.apply(wireLine{Kind: "battery", SoC: 75, Charging: true})
	out := render(m)
	if !strings.Contains(out, "battery 75%") || !strings.Contains(out, "charging") {
		t.Errorf("battery row = %q", out)
	}
}

// Every body line stays inside the terminal width contract the art
// assumes; a decode of an oversized settings map must not wrap the art.
func TestUpdateDecayLoop(t *testing.T) {
	m := testModel()
	m.apply(wireLine{Kind: "input", Wheel: 1})
	_, cmd := m.Update(lineMsg(wireLine{Kind: "input", Wheel: 1}))
	if cmd == nil {
		t.Fatal("input line must arm the decay tick + reader wait")
	}
	if !m.decayArmed {
		t.Error("decay not armed")
	}
	// after expiry the decay loop stops re-arming
	m.now = time.Now().Add(time.Hour)
	_, _ = m.Update(decayMsg(m.now))
	if m.decayArmed {
		t.Error("decay loop must disarm once nothing is lit")
	}
	var _ tea.Model = m
}
