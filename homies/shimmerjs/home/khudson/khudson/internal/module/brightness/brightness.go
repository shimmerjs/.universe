// Package brightness reads DDC state (luminance, contrast) of an external
// display via the m1ddc CLI. Pure data mapper, loud errors, ctx-bounded
// shell-outs.
package brightness

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// Mod implements module.Module.
type Mod struct{}

func (Mod) Name() string { return "brightness" }

func (Mod) Poll(ctx context.Context, params map[string]any) (module.Data, error) {
	bin := "m1ddc"
	if v, ok := params["bin"].(string); ok && v != "" {
		bin = v
	}
	want := "XENEON EDGE"
	if v, ok := params["display"].(string); ok && v != "" {
		want = v
	}

	listOut, err := run(ctx, bin, "display", "list")
	if err != nil {
		return module.Data{}, err
	}
	displays := parseDisplayList(listOut)
	idx := 0
	name := ""
	for _, d := range displays {
		if strings.Contains(strings.ToLower(d.name), strings.ToLower(want)) {
			idx, name = d.index, d.name
			break
		}
	}
	if idx == 0 {
		return module.Data{}, fmt.Errorf("%s: display %q not found in %d displays", bin, want, len(displays))
	}

	var rows []module.Row
	for _, attr := range []string{"luminance", "contrast"} {
		out, err := run(ctx, bin, "display", strconv.Itoa(idx), "get", attr)
		if err != nil {
			return module.Data{}, err
		}
		v, err := parseIntOutput(out)
		if err != nil {
			return module.Data{}, fmt.Errorf("%s get %s: %w", bin, attr, err)
		}
		rows = append(rows, module.Gauge(attr, float64(v)/100, strconv.Itoa(v)))
	}
	rows = append(rows, module.Row{Kind: module.RowDivider})
	rows = append(rows, module.Row{Kind: module.RowKV, Key: "display", Value: name, Style: module.StyleDim})

	return module.Data{Title: "brightness", Rows: rows}, nil
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}

type display struct {
	index int
	name  string
}

// parseDisplayList parses `m1ddc display list` lines like
// "[2] XENEON EDGE (5DDA2C4F-...)".
func parseDisplayList(out string) []display {
	var ds []display
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[") {
			continue
		}
		bracket, rest, ok := strings.Cut(line[1:], "]")
		if !ok {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(bracket))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(rest)
		if i := strings.LastIndex(name, "("); i >= 0 {
			name = strings.TrimSpace(name[:i])
		}
		if name == "" {
			continue
		}
		ds = append(ds, display{index: idx, name: name})
	}
	return ds
}

// parseIntOutput parses a bare-integer output like "62\n".
func parseIntOutput(out string) (int, error) {
	s := strings.TrimSpace(out)
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("expected integer, got %q", s)
	}
	return v, nil
}
