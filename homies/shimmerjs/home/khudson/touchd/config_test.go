package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadModuleConfig(t *testing.T) {
	// flag absent: legacy default, both sources on, no logiretch settings
	got, logi, err := loadModuleConfig("")
	if err != nil || !got["edge"] || !got["moonlander"] {
		t.Fatalf("legacy default = %v, %v", got, err)
	}
	if got["logiretch"] || logi != nil {
		t.Fatalf("legacy default enabled logiretch: modules=%v logi=%v", got, logi)
	}

	// keyboard-only host: edge stays off
	got, _, err = loadModuleConfig(writeConfig(t, `{"modules":{"edge":false,"moonlander":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got["edge"] || !got["moonlander"] || len(got) != 1 {
		t.Fatalf("keyboard-only config = %v", got)
	}

	// every config problem fails fast: a silent fallback to the default
	// would reinstate a perpetual Edge poll on a keyboard-only host
	for name, path := range map[string]string{
		"missing file":     filepath.Join(t.TempDir(), "missing.json"),
		"invalid json":     writeConfig(t, `{"modules":`),
		"unknown module":   writeConfig(t, `{"modules":{"moonlander":true,"moonlandr":true}}`),
		"unknown field":    writeConfig(t, `{"modules":{"edge":true},"extra":1}`),
		"unknown logi key": writeConfig(t, `{"modules":{"logiretch":true},"logiretch":{"dpu":800}}`),
		"trailing data":    writeConfig(t, `{"modules":{"edge":true}} {}`),
		"none enabled":     writeConfig(t, `{"modules":{"edge":false,"moonlander":false}}`),
	} {
		if _, _, err := loadModuleConfig(path); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A partial logiretch block decodes into a *logiConfig whose optional pointers
// distinguish present from absent: dpi/takeoverReset set here, everything else
// nil (leave the device alone).
func TestLoadLogiretchConfig(t *testing.T) {
	enabled, logi, err := loadModuleConfig(writeConfig(t, `{"modules":{"logiretch":true},"logiretch":{"dpi":1600,"takeoverReset":true,"smartShift":{"threshold":30},"buttons":[{"cid":416,"remap":265}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !enabled["logiretch"] {
		t.Fatalf("logiretch not enabled: %v", enabled)
	}
	if logi == nil {
		t.Fatal("logiretch block did not decode")
	}
	if logi.DPI == nil || *logi.DPI != 1600 {
		t.Fatalf("dpi = %v, want 1600", logi.DPI)
	}
	if logi.TakeoverReset == nil || !*logi.TakeoverReset {
		t.Fatalf("takeoverReset = %v, want true", logi.TakeoverReset)
	}
	if logi.HiresWheel != nil || logi.Haptic != nil || logi.BatteryPollSec != nil {
		t.Fatalf("absent fields decoded non-nil: %+v", logi)
	}
	if logi.SmartShift == nil || logi.SmartShift.Threshold == nil || *logi.SmartShift.Threshold != 30 || logi.SmartShift.Mode != nil {
		t.Fatalf("smartShift subfields wrong: %+v", logi.SmartShift)
	}
	if len(logi.Buttons) != 1 || logi.Buttons[0].CID != 416 || logi.Buttons[0].Remap != 265 {
		t.Fatalf("buttons = %+v", logi.Buttons)
	}
}

// The MAXIMAL exported shape (every schema field, exactly as `cue export`
// emits it) decodes without tripping DisallowUnknownFields -- this pins the
// CUE schema field names against the Go json tags at test time.
func TestLoadLogiretchConfigFull(t *testing.T) {
	full := `{"modules":{"edge":true,"moonlander":false,"logiretch":true},` +
		`"logiretch":{"batteryPollSec":90,"buttons":[{"cid":416,"remap":265}],"dpi":1600,` +
		`"haptic":50,"hiresWheel":false,"smartShift":{"mode":0,"threshold":30,"torque":40},` +
		`"takeoverReset":true,"thumbwheel":true}}`
	_, logi, err := loadModuleConfig(writeConfig(t, full))
	if err != nil {
		t.Fatalf("full config rejected: %v", err)
	}
	if logi == nil || logi.Haptic == nil || *logi.Haptic != 50 || logi.BatteryPollSec == nil ||
		*logi.BatteryPollSec != 90 || logi.HiresWheel == nil || *logi.HiresWheel {
		t.Fatalf("full config decode: %+v", logi)
	}
	if logi.SmartShift == nil || logi.SmartShift.Torque == nil || *logi.SmartShift.Torque != 40 {
		t.Fatalf("smartShift torque decode: %+v", logi.SmartShift)
	}
	if logi.Thumbwheel == nil || !*logi.Thumbwheel {
		t.Fatalf("thumbwheel decode: %v", logi.Thumbwheel)
	}
}

// A bad -config makes run itself error (main exits nonzero on that path),
// before any socket bind or HID work.
func TestRunConfigFailFast(t *testing.T) {
	ctx := context.Background()
	if err := run(ctx, options{daemon: true, config: filepath.Join(t.TempDir(), "missing.json")}); err == nil {
		t.Fatal("unreadable -config did not fail run")
	}
	if err := run(ctx, options{daemon: true, config: writeConfig(t, `not json`)}); err == nil {
		t.Fatal("invalid -config did not fail run")
	}
}
