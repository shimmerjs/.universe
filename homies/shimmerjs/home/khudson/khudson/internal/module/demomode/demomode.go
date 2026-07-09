// Package demomode is a synthetic showcase module: fixed rows in every
// primitive and style so the renderer can be eyeballed. No shell-outs; a
// "seed" param varies the gauges deterministically, so identical params
// always yield identical Data.
package demomode

import (
	"context"
	"fmt"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// Mod implements module.Module.
type Mod struct{}

func (Mod) Name() string { return "demo-mode" }

func (Mod) Poll(_ context.Context, params map[string]any) (module.Data, error) {
	seed := seedFrom(params)
	fracs := gaugeFracs(seed)

	rows := []module.Row{
		module.Text("plain text row"),
		{Kind: module.RowText, Text: "dim text row", Style: module.StyleDim},
		{Kind: module.RowText, Text: "accent text row", Style: module.StyleAccent},
		{Kind: module.RowText, Text: "warn text row", Style: module.StyleWarn},
		module.KV("host", "khudson-demo"),
		module.KV("state", "steady"),
		module.Gauge("low", fracs[0], fmt.Sprintf("%.0f%%", fracs[0]*100)),
		module.Gauge("mid", fracs[1], fmt.Sprintf("%.0f%%", fracs[1]*100)),
		module.Gauge("high", fracs[2], fmt.Sprintf("%.0f%%", fracs[2]*100)),
		{Kind: module.RowDivider},
		{Kind: module.RowText, Text: fmt.Sprintf("seed %d", seed), Style: module.StyleDim},
	}

	return module.Data{Title: "demo", Rows: rows}, nil
}

// seedFrom reads params["seed"], tolerating the numeric types config
// decoding produces.
func seedFrom(params map[string]any) int64 {
	return int64(module.IntParam(params, "seed", 0))
}

// gaugeFracs derives the three gauge fills from seed. Seed 0 keeps the
// canonical 0.25/0.5/0.9 showcase values; other seeds shift each frac by
// a fixed function of the seed, clamped to [0.05, 1].
func gaugeFracs(seed int64) [3]float64 {
	fracs := [3]float64{0.25, 0.5, 0.9}
	for i := range fracs {
		fracs[i] += float64(seed*int64(i+7)%20) / 100
		if fracs[i] < 0.05 {
			fracs[i] = 0.05
		}
		if fracs[i] > 1 {
			fracs[i] = 1
		}
	}
	return fracs
}
