package config

import "testing"

// TestCaffeinateDecode pins the caffeinate block's contract: absent block
// still means ON (the runtime toggle owes its default to Go, see
// CaffeinateOn), an explicit block defaults on, and off is expressible.
func TestCaffeinateDecode(t *testing.T) {
	base := `
widgets: w: {
	title: "w"
	glyph: "x"
	render: {kind: "native", module: "sysmon"}
}
layouts: main: {kind: "dock-grid", tiles: ["w"]}
layout: "main"
`
	t.Run("absent block is nil and ON", func(t *testing.T) {
		c, err := Load("test.cue", []byte(base))
		if err != nil {
			t.Fatal(err)
		}
		if c.Caffeinate != nil {
			t.Fatalf("Caffeinate = %+v, want nil without a block", c.Caffeinate)
		}
		if !c.CaffeinateOn() {
			t.Fatal("CaffeinateOn() = false without a block, want the ON default")
		}
	})

	t.Run("empty block defaults on", func(t *testing.T) {
		c, err := Load("test.cue", []byte(base+"\ncaffeinate: {}\n"))
		if err != nil {
			t.Fatal(err)
		}
		if c.Caffeinate == nil || !c.Caffeinate.On || !c.CaffeinateOn() {
			t.Fatalf("Caffeinate = %+v, want on via the schema default", c.Caffeinate)
		}
	})

	t.Run("explicit off", func(t *testing.T) {
		c, err := Load("test.cue", []byte(base+"\ncaffeinate: on: false\n"))
		if err != nil {
			t.Fatal(err)
		}
		if c.CaffeinateOn() {
			t.Fatal("CaffeinateOn() = true with caffeinate.on: false")
		}
	})

	t.Run("non-bool rejected", func(t *testing.T) {
		if _, err := Load("test.cue", []byte(base+"\ncaffeinate: on: \"yes\"\n")); err == nil {
			t.Fatal("string caffeinate.on vetted clean")
		}
	})
}
