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
	// flag absent: legacy default, both sources on
	got, err := loadModuleConfig("")
	if err != nil || !got["edge"] || !got["moonlander"] {
		t.Fatalf("legacy default = %v, %v", got, err)
	}

	// keyboard-only host: edge stays off
	got, err = loadModuleConfig(writeConfig(t, `{"modules":{"edge":false,"moonlander":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got["edge"] || !got["moonlander"] || len(got) != 1 {
		t.Fatalf("keyboard-only config = %v", got)
	}

	// every config problem fails fast: a silent fallback to the default
	// would reinstate a perpetual Edge poll on a keyboard-only host
	for name, path := range map[string]string{
		"missing file":   filepath.Join(t.TempDir(), "missing.json"),
		"invalid json":   writeConfig(t, `{"modules":`),
		"unknown module": writeConfig(t, `{"modules":{"moonlander":true,"moonlandr":true}}`),
		"unknown field":  writeConfig(t, `{"modules":{"edge":true},"extra":1}`),
		"trailing data":  writeConfig(t, `{"modules":{"edge":true}} {}`),
		"none enabled":   writeConfig(t, `{"modules":{"edge":false,"moonlander":false}}`),
	} {
		if _, err := loadModuleConfig(path); err == nil {
			t.Errorf("%s: no error", name)
		}
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
