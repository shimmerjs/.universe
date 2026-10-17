package surface

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	logi "github.com/shimmerjs/khudson/khudson/internal/module/logiretch"
	"github.com/shimmerjs/khudson/khudson/provider"
)

func logiModel(w, h int) *model {
	m := newHomeModel(w, h)
	m.cfg.Widgets["logiretch-view"] = config.Widget{ID: "logiretch-view", Title: "logiretch",
		Chrome: true, Render: config.Render{Kind: "native", Module: "logiretch"}}
	l := m.cfg.Layouts["home"]
	l.Regions = []config.Region{{Widget: "logiretch-view", Edge: "fill"}}
	m.cfg.Layouts["home"] = l
	return m
}

func logiData() module.Data {
	sp := func(s string) module.Span { return module.Span{Text: s} }
	return module.Data{Title: "logiretch", Rows: []module.Row{
		{Kind: module.RowSpans, Key: logi.KeyArt, Spans: []module.Span{sp(`      .-""-.`)}},
		{Kind: module.RowSpans, Key: logi.KeyArt, Spans: []module.Span{
			sp("     /"), {Text: "L", Style: module.StyleHighlight}, sp(" | R\\")}},
		{Kind: module.RowSpans, Key: logi.KeyArt, Spans: []module.Span{sp("      '.__.'")}},
		{Kind: module.RowDivider},
		{Kind: module.RowKV, Key: "dpi", Value: "1600 (dev 1600)"},
		{Kind: module.RowKV, Key: "haptic", Value: "off ! device said no", Style: module.StyleWarn},
		{Kind: module.RowDivider},
		{Kind: module.RowSpans, Key: logi.KeyStatus, Spans: []module.Span{sp("link up  bat 65%")}},
	}}
}

// The chrome renderer owns logiretch's geometry: art in its own column,
// readings beside it, the link/battery footer pinned to the bottom edge.
// Before it existed the widget rendered the loud "no chrome renderer" box.
func TestLogiretchRendererLayout(t *testing.T) {
	m := logiModel(196, 24)
	m.widgetData["logiretch-view"] = logiData()

	v := m.View()
	lines := strings.Split(v.Content, "\n")
	for i, l := range lines {
		if lipgloss.Width(l) != 196 {
			t.Fatalf("line %d width = %d, want 196", i, lipgloss.Width(l))
		}
	}
	body := ansi.Strip(v.Content)
	if strings.Contains(body, "no chrome renderer") {
		t.Fatalf("logiretch still falls through to the warn box:\n%s", body)
	}
	for _, want := range []string{"logiretch", ".__.", "dpi", "1600 (dev 1600)", "device said no"} {
		if !strings.Contains(body, want) {
			t.Errorf("view missing %q", want)
		}
	}
	// footer pins to the last body row, above the strip
	foot := ansi.Strip(lines[len(lines)-2])
	if !strings.Contains(foot, "link up") {
		t.Errorf("status footer not on the bottom row: %q", foot)
	}
	// art and readings share a line: the readings must not stack under
	// the mouse on a region this wide
	var paired bool
	for l := range strings.SplitSeq(body, "\n") {
		if strings.Contains(l, "dpi") && strings.Contains(l, `.-""-.`) {
			paired = true
		}
	}
	if !paired {
		t.Error("wide region must set the readings beside the art, not under it")
	}
}

// A narrow column has no room for two columns: the art stacks over the
// readings instead of squeezing them into nothing.
func TestLogiretchRendererStacksWhenNarrow(t *testing.T) {
	m := logiModel(30, 24)
	m.widgetData["logiretch-view"] = logiData()
	body := ansi.Strip(m.View().Content)
	for l := range strings.SplitSeq(body, "\n") {
		if strings.Contains(l, "dpi") && strings.Contains(l, ".__.") {
			t.Errorf("narrow region must stack, not pair: %q", l)
		}
	}
	if !strings.Contains(body, "dpi") {
		t.Errorf("stacked view dropped the readings:\n%s", body)
	}
}

// The viewer is read-only: taps die in the region rather than falling
// through to whatever sits behind it.
func TestLogiretchRendererConsumesTaps(t *testing.T) {
	m := logiModel(196, 24)
	m.widgetData["logiretch-view"] = logiData()
	m.View()
	if !m.resolveTap(40, 10) {
		t.Error("region must consume its taps")
	}
}
