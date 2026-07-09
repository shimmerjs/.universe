package config

import "testing"

// TestThemeDecode pins the theme block's schema contract: defaults fill
// bin/display/luminance, colors must be #rrggbb, and a config without a
// theme block decodes to a nil Theme.
func TestThemeDecode(t *testing.T) {
	base := `
widgets: w: {
	title: "w"
	glyph: "x"
	render: {kind: "native", module: "sysmon"}
}
layouts: main: {kind: "dock-grid", tiles: ["w"]}
layout: "main"
`
	t.Run("defaults", func(t *testing.T) {
		src := base + `
theme: night: colors: background: "#000000"
`
		c, err := Load("test.cue", []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		if c.Theme == nil {
			t.Fatal("theme block decoded to nil")
		}
		if got := c.Theme.Night.Colors["background"]; got != "#000000" {
			t.Fatalf("night background = %q, want #000000", got)
		}
		l := c.Theme.Luminance
		if l.Bin != "m1ddc" || l.Display != "XENEON EDGE" || l.Night != 10 || l.Day != 60 {
			t.Fatalf("luminance defaults = %+v, want m1ddc/XENEON EDGE/10/60", l)
		}
	})

	t.Run("absent block is nil", func(t *testing.T) {
		c, err := Load("test.cue", []byte(base))
		if err != nil {
			t.Fatal(err)
		}
		if c.Theme != nil {
			t.Fatalf("Theme = %+v, want nil without a theme block", c.Theme)
		}
	})

	t.Run("bad hex rejected", func(t *testing.T) {
		src := base + `
theme: night: colors: background: "black"
`
		if _, err := Load("test.cue", []byte(src)); err == nil {
			t.Fatal("non-hex night color vetted clean")
		}
	})
}
