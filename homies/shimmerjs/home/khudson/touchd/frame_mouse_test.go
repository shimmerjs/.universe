package main

import "testing"

// raw report captured from live hardware: tip down at x=1523 y=5861
func TestParseMouseReport(t *testing.T) {
	raw := []byte{0x07, 0x01, 0xF3, 0x05, 0xE5, 0x16, 0x00}
	f, ok := parseMouseReport(42, raw)
	if !ok {
		t.Fatal("parseMouseReport rejected a valid report")
	}
	if f.T != 42 || f.Count != 1 || len(f.Contacts) != 1 {
		t.Fatalf("frame envelope wrong: %+v", f)
	}
	c := f.Contacts[0]
	if !c.Tip || c.X != 1523 || c.Y != 5861 || c.ID != 0 {
		t.Fatalf("contact wrong: %+v", c)
	}

	lift := []byte{0x07, 0x00, 0xF3, 0x05, 0xE5, 0x16, 0x00}
	f, ok = parseMouseReport(43, lift)
	if !ok || f.Count != 0 || f.Contacts[0].Tip {
		t.Fatalf("lift not decoded: %+v ok=%v", f, ok)
	}

	if _, ok := parseMouseReport(44, []byte{0x0D, 0x01}); ok {
		t.Fatal("accepted wrong report id")
	}
	if _, ok := parseReport(45, raw); !ok {
		t.Fatal("parseReport dispatch missed mouse report")
	}
}
