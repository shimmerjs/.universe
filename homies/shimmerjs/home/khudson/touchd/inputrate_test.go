package main

import (
	"strings"
	"testing"
)

// A source that has opened must stay in the line at zero. Silence is the
// observation the wedge forensics turn on: a source vanishing from the record
// is indistinguishable from a source that never existed.
func TestInputRateDrainKeepsSilentSources(t *testing.T) {
	r := newInputRate()
	keys := r.source("moonlander")
	r.source("edge-digitizer")
	keys.Add(3)

	if got, want := r.drain(), " edge-digitizer=0 moonlander=3"; got != want {
		t.Fatalf("drain = %q, want %q", got, want)
	}
	// counts are per interval, so the drain must zero them
	if got, want := r.drain(), " edge-digitizer=0 moonlander=0"; got != want {
		t.Fatalf("second drain = %q, want %q", got, want)
	}
}

// Before any collection opens there is nothing to say, and the ticker must
// stay quiet rather than log an empty line every interval.
func TestInputRateDrainEmptyBeforeAnySource(t *testing.T) {
	if got := newInputRate().drain(); got != "" {
		t.Fatalf("drain with no sources = %q, want empty", got)
	}
}

// Sources are ordered so successive lines diff cleanly by eye.
func TestInputRateDrainIsOrdered(t *testing.T) {
	r := newInputRate()
	for _, n := range []string{"moonlander", "edge-mouse", "edge-digitizer"} {
		r.source(n)
	}
	got := r.drain()
	want := []string{"edge-digitizer", "edge-mouse", "moonlander"}
	at := -1
	for _, name := range want {
		i := strings.Index(got, name)
		if i <= at {
			t.Fatalf("drain = %q, want sources in order %v", got, want)
		}
		at = i
	}
}

// A nil counter is the spike/test path: readLoop must tolerate it.
func TestEnvInputSourceNilWithoutRate(t *testing.T) {
	if c := (Env{}).InputSource("moonlander"); c != nil {
		t.Fatalf("InputSource without a rate = %v, want nil", c)
	}
}
