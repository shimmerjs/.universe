// Package cheatsheets is a config-driven reference card: static sections
// of key/value entries straight from params, no external state polled.
package cheatsheets

import (
	"context"
	"fmt"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// maxRows keeps the payload inside a ~20-row dock panel.
const maxRows = 20

// Mod implements module.Module.
type Mod struct{}

func (Mod) Name() string { return "cheatsheets" }

func (Mod) Poll(_ context.Context, params map[string]any) (module.Data, error) {
	secs, err := parseSections(params)
	if err != nil {
		return module.Data{}, err
	}
	return module.Data{Title: "cheatsheets", Rows: render(secs)}, nil
}

type section struct {
	title   string
	entries []entry
}

type entry struct {
	key   string
	value string
}

// parseSections validates the JSON-decoded params shape:
// { sections: [ { title: string, entries: [ { key: string, value: string } ] } ] }.
func parseSections(params map[string]any) ([]section, error) {
	raw, ok := params["sections"]
	if !ok {
		return nil, fmt.Errorf("cheatsheets: params.sections missing (want list of {title, entries})")
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("cheatsheets: params.sections: want list, got %T", raw)
	}
	secs := make([]section, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cheatsheets: sections[%d]: want object, got %T", i, item)
		}
		title, ok := m["title"].(string)
		if !ok {
			return nil, fmt.Errorf("cheatsheets: sections[%d].title: want string, got %T", i, m["title"])
		}
		rawEntries, ok := m["entries"].([]any)
		if !ok {
			return nil, fmt.Errorf("cheatsheets: sections[%d].entries: want list, got %T", i, m["entries"])
		}
		sec := section{title: title, entries: make([]entry, 0, len(rawEntries))}
		for j, re := range rawEntries {
			em, ok := re.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("cheatsheets: sections[%d].entries[%d]: want object, got %T", i, j, re)
			}
			k, ok := em["key"].(string)
			if !ok {
				return nil, fmt.Errorf("cheatsheets: sections[%d].entries[%d].key: want string, got %T", i, j, em["key"])
			}
			v, ok := em["value"].(string)
			if !ok {
				return nil, fmt.Errorf("cheatsheets: sections[%d].entries[%d].value: want string, got %T", i, j, em["value"])
			}
			sec.entries = append(sec.entries, entry{key: k, value: v})
		}
		secs = append(secs, sec)
	}
	return secs, nil
}

// render emits an accent title row plus KV rows per section, a divider
// between sections, capped at maxRows with a dim "+N more" tail.
func render(secs []section) []module.Row {
	var rows []module.Row
	for i, sec := range secs {
		if i > 0 {
			rows = append(rows, module.Row{Kind: module.RowDivider})
		}
		rows = append(rows, module.Row{Kind: module.RowText, Text: sec.title, Style: module.StyleAccent})
		for _, e := range sec.entries {
			rows = append(rows, module.KV(e.key, e.value))
		}
	}
	if len(rows) <= maxRows {
		return rows
	}
	dropped := 0
	for _, r := range rows[maxRows-1:] {
		if r.Kind != module.RowDivider {
			dropped++
		}
	}
	return append(rows[:maxRows-1],
		module.Row{Kind: module.RowText, Text: fmt.Sprintf("+%d more", dropped), Style: module.StyleDim})
}
