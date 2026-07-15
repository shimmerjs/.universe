package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
)

// daemonConfig is the -config wire shape, plain JSON exported from the
// host's CUE at nix build time:
//
//	{"modules": {"edge": true, "moonlander": true}}
//
// Unknown fields, unknown module names, and an empty enabled set are all
// errors: a config problem must exit nonzero, because a keyboard-only host
// silently falling back to the legacy default would reinstate a perpetual
// Edge poll.
type daemonConfig struct {
	Modules map[string]bool `json:"modules"`
}

// moduleNames are the registerable modules, the accepted config keys.
var moduleNames = []string{"edge", "moonlander"}

// defaultModules is the legacy no-flag default: both sources on.
func defaultModules() map[string]bool {
	return map[string]bool{"edge": true, "moonlander": true}
}

// loadModuleConfig resolves the enabled-module set: the config file when the
// flag was passed (fail-fast on any problem), the legacy default otherwise.
func loadModuleConfig(path string) (map[string]bool, error) {
	if path == "" {
		return defaultModules(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg daemonConfig
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("config %s: trailing data after the config object", path)
	}
	enabled := map[string]bool{}
	for name, on := range cfg.Modules {
		if !slices.Contains(moduleNames, name) {
			return nil, fmt.Errorf("config %s: unknown module %q", path, name)
		}
		if on {
			enabled[name] = true
		}
	}
	if len(enabled) == 0 {
		return nil, fmt.Errorf("config %s: no modules enabled", path)
	}
	return enabled, nil
}
