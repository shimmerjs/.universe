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
//	{"modules": {"edge": true, "moonlander": true, "logiretch": true},
//	 "logiretch": {"dpi": 1600, "takeoverReset": true}}
//
// Unknown fields, unknown module names, and an empty enabled set are all
// errors: a config problem must exit nonzero, because a keyboard-only host
// silently falling back to the legacy default would reinstate a perpetual
// Edge poll.
type daemonConfig struct {
	Modules   map[string]bool `json:"modules"`
	Logiretch *logiConfig     `json:"logiretch,omitempty"`
}

// logiConfig is the logiretch module's desired device state. Every optional
// field is a POINTER because presence is load-bearing: absent = leave the
// device alone, and a plain int/bool cannot tell absent from a zero value. A
// nil logiConfig (no "logiretch" block) means battery-only with the
// takeoverReset default.
type logiConfig struct {
	DPI            *int              `json:"dpi,omitempty"`
	SmartShift     *smartShiftConfig `json:"smartShift,omitempty"`
	HiresWheel     *bool             `json:"hiresWheel,omitempty"`
	Thumbwheel     *bool             `json:"thumbwheel,omitempty"`
	Haptic         *int              `json:"haptic,omitempty"`
	Buttons        []buttonRemap     `json:"buttons,omitempty"`
	TakeoverReset  *bool             `json:"takeoverReset,omitempty"`
	BatteryPollSec *int              `json:"batteryPollSec,omitempty"`
}

// smartShiftConfig is the 0x2111 SMART_SHIFT_ENHANCED target; each subfield is
// a pointer so an absent one is left untouched on the device.
type smartShiftConfig struct {
	Mode      *int `json:"mode,omitempty"`
	Threshold *int `json:"threshold,omitempty"`
	Torque    *int `json:"torque,omitempty"`
}

// buttonRemap is one 0x1B04 remap: CID to target CID. A CID listed here is
// remapped by the takeoverReset walk instead of being divert-cleared.
type buttonRemap struct {
	CID   int `json:"cid"`
	Remap int `json:"remap"`
}

// moduleNames are the registerable modules, the accepted config keys.
var moduleNames = []string{"edge", "moonlander", "logiretch"}

// defaultModules is the legacy no-flag default: both HUD sources on. logiretch
// is additive and never in the no-config default.
func defaultModules() map[string]bool {
	return map[string]bool{"edge": true, "moonlander": true}
}

// loadModuleConfig resolves the enabled-module set and the decoded logiretch
// settings: the config file when the flag was passed (fail-fast on any
// problem), the legacy default otherwise. The returned *logiConfig is nil
// unless the config carries a "logiretch" block.
func loadModuleConfig(path string) (map[string]bool, *logiConfig, error) {
	if path == "" {
		return defaultModules(), nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg daemonConfig
	if err := dec.Decode(&cfg); err != nil {
		return nil, nil, fmt.Errorf("config %s: %w", path, err)
	}
	if dec.More() {
		return nil, nil, fmt.Errorf("config %s: trailing data after the config object", path)
	}
	enabled := map[string]bool{}
	for name, on := range cfg.Modules {
		if !slices.Contains(moduleNames, name) {
			return nil, nil, fmt.Errorf("config %s: unknown module %q", path, name)
		}
		if on {
			enabled[name] = true
		}
	}
	if len(enabled) == 0 {
		return nil, nil, fmt.Errorf("config %s: no modules enabled", path)
	}
	return enabled, cfg.Logiretch, nil
}
