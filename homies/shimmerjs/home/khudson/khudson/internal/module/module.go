// Package module defines the native-widget contract (DESIGN-v2: the bus
// fetches, the dock renders). A Module turns a poll into a view model made
// of shared render primitives; per-module pixels live in the dock's ONE
// primitive renderer, so modules stay pure data mappers. Exotic surfaces
// (kitty graphics, canvases) get their own milestone; they do not stretch
// this contract.
package module

import (
	"context"
	"fmt"
	"time"
)

// Module is one native backend compiled into khudson. Modules are singletons:
// one value serves every widget that names the module, and it MAY keep
// state across Poll calls (e.g. ring buffers backing RowSeries history).
type Module interface {
	// Name matches the schema's #Module vocabulary.
	Name() string
	// Poll fetches fresh data. Implementations shell out or hit APIs; the
	// bus enforces cadence (schema floor 1s) and single-flight, and ctx
	// carries the per-poll timeout. Errors surface on the tile -- loud,
	// not silent -- so return them, don't log-and-drop.
	Poll(ctx context.Context, params map[string]any) (Data, error)
}

// Data is a widget view model.
type Data struct {
	Title string `json:"title,omitempty"`
	Rows  []Row  `json:"rows,omitempty"`
	// Attention marks a view with a session/item awaiting input; the dock
	// animates the carrying region's frame while it is set.
	Attention bool `json:"attention,omitempty"`
}

// Row kinds.
const (
	RowText    = "text"
	RowKV      = "kv"
	RowGauge   = "gauge"
	RowDivider = "divider"
	RowSeries  = "series"
	// RowResource is one resource cluster: label, current-fraction gauge,
	// history sparkline, current reading. cpu, mem, and disk volumes all
	// render through it so the home screen stays visually uniform.
	RowResource = "resource"
	// RowSpans is one line of independently styled runs (Row.Spans),
	// concatenated left to right and truncated to the panel width.
	RowSpans = "spans"
)

// Row styles (dock palette names, not colors: modules stay theme-blind).
const (
	StyleDim     = "dim"
	StyleAccent  = "accent"
	StyleWarn    = "warn"
	// StyleHighlight is the emphasis style for inline glyph+number pairs.
	StyleHighlight = "highlight"
	// StyleTitle is spans-only: the span renders in the row's base style,
	// bold, so a line's title run stays distinct in either tone.
	StyleTitle = "title"
)

// Span is one styled run within a RowSpans row.
type Span struct {
	Text  string `json:"text"`
	Style string `json:"style,omitempty"`
	// Ident is an optional identity key (data-not-style): the dock
	// resolves it to a stable per-key hue at render time, composed with
	// Style; modules stay theme-blind.
	Ident string `json:"ident,omitempty"`
}

// Row is one rendered line.
type Row struct {
	Kind   string    `json:"kind"`
	Text   string    `json:"text,omitempty"`   // text
	Key    string    `json:"key,omitempty"`    // kv, gauge label, series label
	Value  string    `json:"value,omitempty"`  // kv, gauge suffix, series current
	Frac   float64   `json:"frac,omitempty"`   // gauge fill 0..1
	Series []float64 `json:"series,omitempty"` // series samples 0..1, oldest first
	Spans  []Span    `json:"spans,omitempty"`  // spans
	Style  string    `json:"style,omitempty"`
	// MinHeight is the row's height in lines; 0 means 1. Chrome widgets
	// declare taller rows with it.
	MinHeight int `json:"minHeight,omitempty"`
	// Act makes the row a button: tapping it runs this argv on the bus
	// host (dock-mirror rows activate apps the way the real Dock would).
	Act []string `json:"act,omitempty"`
}

// IntParam reads an int-valued param, tolerating the numeric types config
// decoding produces (CUE ints decode as int64). Missing or non-numeric
// means def.
func IntParam(params map[string]any, key string, def int) int {
	switch v := params[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return def
	}
}

// Age humanizes now-t: "now", "5m", "3h", "2d", "6w".
func Age(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dw", int(d.Hours()/(24*7)))
	}
}

// Text is shorthand for a plain text row.
func Text(s string) Row { return Row{Kind: RowText, Text: s} }

// KV is shorthand for a key/value row.
func KV(k, v string) Row { return Row{Kind: RowKV, Key: k, Value: v} }

// SpansRow is shorthand for a spans row.
func SpansRow(spans ...Span) Row { return Row{Kind: RowSpans, Spans: spans} }

// Gauge is shorthand for a labeled fill bar.
func Gauge(label string, frac float64, suffix string) Row {
	frac = min(max(frac, 0), 1)
	return Row{Kind: RowGauge, Key: label, Frac: frac, Value: suffix}
}

// MaxSeries caps emitted samples per series: one braille cell per sample,
// so this bounds the widest history spark the dock draws. Sized for display
// width over wire cost -- every sample is per-poll payload; rings may still
// hold deeper history than a row can show.
const MaxSeries = 120

// Series is shorthand for a sparkline row: samples normalized 0..1, oldest
// first; value is the current reading in human units. Samples are clamped,
// capped at MaxSeries (newest kept), and copied so callers may keep
// mutating their ring buffers.
func Series(key string, samples []float64, value string) Row {
	if len(samples) > MaxSeries {
		samples = samples[len(samples)-MaxSeries:]
	}
	s := make([]float64, len(samples))
	for i, v := range samples {
		s[i] = min(max(v, 0), 1)
	}
	return Row{Kind: RowSeries, Key: key, Series: s, Value: value}
}

// Resource is shorthand for a resource cluster row: frac is the current
// utilization 0..1, samples the normalized history oldest first, value the
// current reading in human units. Frac and samples are clamped, and samples
// copied so callers may keep mutating their ring buffers.
func Resource(key string, frac float64, samples []float64, value string) Row {
	frac = min(max(frac, 0), 1)
	r := Series(key, samples, value)
	r.Kind = RowResource
	r.Frac = frac
	return r
}

// History sizing: ring capacity is params.window divided by the sample
// cadence, capped so a huge window cannot balloon memory.
const (
	defaultWindow = 6 * time.Hour
	assumedPoll   = 5 * time.Second
	maxHistCap    = 86400 // 24h at a 1s poll (~675 KiB/series of float64)
)

// HistCadence is the history rings' sample cadence: the scheduler injects
// the widget's real poll interval as params["poll-interval"] (a
// time.Duration -- runtime-only, never schema'd config, like the resources
// composite's "cpu-util"); direct Poll calls without it fall back to the
// schema's native-widget default.
func HistCadence(params map[string]any) time.Duration {
	if d, ok := params["poll-interval"].(time.Duration); ok && d > 0 {
		return d
	}
	return assumedPoll
}

// HistWindow reads params.window (duration string, default "6h") and
// returns the history ring capacity (window / HistCadence) plus the hint
// resource Row keys carry per the resources contract ("cpu 6h").
// Unparsable or non-positive windows fall back to the default.
func HistWindow(params map[string]any) (int, string) {
	window, hint := defaultWindow, "6h"
	if s, ok := params["window"].(string); ok {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			window, hint = d, s
		}
	}
	n := min(max(int(window/HistCadence(params)), 1), maxHistCap)
	return n, hint
}

// HistState is one persisted history series: the samples oldest first
// (normalized 0..1; float32 halves the snapshot), the cadence they were
// sampled at, and the newest sample's unix time.
type HistState struct {
	Cadence  time.Duration
	LastUnix int64
	Samples  []float32
}

// Persistent is implemented by modules whose history rings survive bus
// restarts (internal/module/histsnap). HistSnapshot returns every series
// keyed by name ("cpu", "mem", "disk/<vol>"); HistRestore installs the
// entries the module recognizes and ignores the rest, so callers hand
// every implementer the same merged map. Ring itself stays dumb.
type Persistent interface {
	HistSnapshot() map[string]HistState
	HistRestore(map[string]HistState)
}

// SnapRing copies r's samples into HistState form; nil r means none.
func SnapRing(r *Ring) []float32 {
	if r == nil {
		return nil
	}
	s := r.Samples()
	out := make([]float32, len(s))
	for i, v := range s {
		out[i] = float32(v)
	}
	return out
}

// RestoreRing is a ring holding exactly samples; the next poll's ResizeRing
// re-caps it to the configured window, keeping the newest samples.
func RestoreRing(samples []float32) *Ring {
	n := max(len(samples), 1)
	r := NewRing(n)
	for _, v := range samples {
		r.Push(float64(v))
	}
	return r
}

// ResizeRing returns a ring of capacity n holding r's newest samples, or r
// itself when its capacity already matches (nil r means a fresh ring).
// Rings are sized at poll time -- the window is a param -- so capacity can
// change on config reload.
func ResizeRing(r *Ring, n int) *Ring {
	n = max(n, 1)
	if r != nil && len(r.buf) == n {
		return r
	}
	nr := NewRing(n)
	if r == nil {
		return nr
	}
	s := r.Samples()
	if len(s) > n {
		s = s[len(s)-n:]
	}
	for _, v := range s {
		nr.Push(v)
	}
	return nr
}

// BucketMax downsamples to at most n samples by taking the max of each
// bucket. Series hard-caps emission at MaxSeries by dropping the oldest
// samples, so window-deep rings MUST be bucketed before emission or the
// cap would blindly truncate a 6h window to its newest 60 samples; max
// per bucket keeps spikes visible through the squeeze.
func BucketMax(samples []float64, n int) []float64 {
	if n <= 0 || len(samples) <= n {
		return samples
	}
	out := make([]float64, n)
	for i := range out {
		lo, hi := i*len(samples)/n, (i+1)*len(samples)/n
		m := samples[lo]
		for _, v := range samples[lo+1 : hi] {
			if v > m {
				m = v
			}
		}
		out[i] = m
	}
	return out
}
