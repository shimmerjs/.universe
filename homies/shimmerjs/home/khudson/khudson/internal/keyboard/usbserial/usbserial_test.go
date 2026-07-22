package usbserial

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// needIoreg skips when the exec'd reader's ioreg CLI is missing (the nix
// checkPhase sandbox has no host binaries).
func needIoreg(t *testing.T) {
	t.Helper()
	if _, err := ioregBin(); err != nil {
		t.Skipf("ioreg: %v", err)
	}
}

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// The fixture is a trimmed real capture: hubs and the Edge surround the
// Moonlander block, whose serial carries the layout/revision pair.
func TestParseFindsMoonlander(t *testing.T) {
	id, err := parse(openFixture(t, "ioreg_moonlander.txt"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if id.LayoutID != "bqMJp" || id.RevisionID != "9DYwNW" {
		t.Errorf("identity = %q/%q, want bqMJp/9DYwNW", id.LayoutID, id.RevisionID)
	}
}

// No ZSA vendor id on the bus (unplugged, or mid-flash in the DFU
// bootloader) is the ErrNotPresent state, not a parse failure.
func TestParseAbsent(t *testing.T) {
	_, err := parse(openFixture(t, "ioreg_absent.txt"))
	if !errors.Is(err, ErrNotPresent) {
		t.Fatalf("err = %v, want ErrNotPresent", err)
	}
}

// A ZSA device whose serial is not the Oryx <layout>/<revision> shape
// (custom firmware) surfaces as an explicit error naming the serial.
func TestParseNonOryxSerial(t *testing.T) {
	dump := strings.NewReader(`+-o Moonlander Mark I@02110000  <class IOUSBHostDevice>
    {
      "USB Serial Number" = "CUSTOM-01"
      "USB Product Name" = "Moonlander Mark I"
      "idVendor" = 12951
    }
`)
	_, err := parse(dump)
	if err == nil || !strings.Contains(err.Error(), "CUSTOM-01") {
		t.Fatalf("err = %v, want serial-shape error naming CUSTOM-01", err)
	}
}

func TestSplitSerial(t *testing.T) {
	for _, tc := range []struct {
		in       string
		lay, rev string
		ok       bool
	}{
		{"bqMJp/9DYwNW", "bqMJp", "9DYwNW", true},
		{"bqMJp", "", "", false},
		{"/9DYwNW", "", "", false},
		{"bqMJp/", "", "", false},
		{"bq-Jp/9DYwNW", "", "", false},
	} {
		lay, rev, ok := splitSerial(tc.in)
		if lay != tc.lay || rev != tc.rev || ok != tc.ok {
			t.Errorf("splitSerial(%q) = %q,%q,%v want %q,%q,%v", tc.in, lay, rev, ok, tc.lay, tc.rev, tc.ok)
		}
	}
}

// Live smoke against the host bus: skipped without ioreg, skipped when no
// board is plugged in; with one present the identity halves must be set.
func TestReadLive(t *testing.T) {
	needIoreg(t)
	id, err := Read(context.Background())
	if errors.Is(err, ErrNotPresent) {
		t.Skip("no ZSA keyboard on the bus")
	}
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if id.LayoutID == "" || id.RevisionID == "" {
		t.Errorf("identity = %+v, want both hashes set", id)
	}
}
