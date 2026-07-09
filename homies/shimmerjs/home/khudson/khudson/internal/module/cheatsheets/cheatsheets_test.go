package cheatsheets

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

func wellFormed() map[string]any {
	return map[string]any{
		"sections": []any{
			map[string]any{
				"title": "git",
				"entries": []any{
					map[string]any{"key": "amend", "value": "git commit --amend"},
					map[string]any{"key": "undo", "value": "git reset HEAD~1"},
				},
			},
			map[string]any{
				"title": "tmux",
				"entries": []any{
					map[string]any{"key": "detach", "value": "C-b d"},
				},
			},
		},
	}
}

func TestPollRendersSections(t *testing.T) {
	data, err := Mod{}.Poll(context.Background(), wellFormed())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if data.Title != "cheatsheets" {
		t.Errorf("Title = %q, want %q", data.Title, "cheatsheets")
	}
	want := []module.Row{
		{Kind: module.RowText, Text: "git", Style: module.StyleAccent},
		module.KV("amend", "git commit --amend"),
		module.KV("undo", "git reset HEAD~1"),
		{Kind: module.RowDivider},
		{Kind: module.RowText, Text: "tmux", Style: module.StyleAccent},
		module.KV("detach", "C-b d"),
	}
	if !reflect.DeepEqual(data.Rows, want) {
		t.Errorf("Rows = %#v, want %#v", data.Rows, want)
	}
}

func TestPollMalformed(t *testing.T) {
	cases := []struct {
		name    string
		params  map[string]any
		wantSub string
	}{
		{"nil params", nil, "params.sections missing"},
		{"sections not list", map[string]any{"sections": "nope"}, "params.sections: want list"},
		{"section not object", map[string]any{"sections": []any{42.0}}, "sections[0]: want object"},
		{"title not string",
			map[string]any{"sections": []any{map[string]any{"title": 1.0, "entries": []any{}}}},
			"sections[0].title: want string"},
		{"entries missing",
			map[string]any{"sections": []any{map[string]any{"title": "x"}}},
			"sections[0].entries: want list"},
		{"entry not object",
			map[string]any{"sections": []any{map[string]any{"title": "x", "entries": []any{"y"}}}},
			"sections[0].entries[0]: want object"},
		{"entry key not string",
			map[string]any{"sections": []any{map[string]any{"title": "x",
				"entries": []any{map[string]any{"key": 2.0, "value": "v"}}}}},
			"sections[0].entries[0].key: want string"},
		{"entry value not string",
			map[string]any{"sections": []any{map[string]any{"title": "x",
				"entries": []any{map[string]any{"key": "k", "value": 3.0}}}}},
			"sections[0].entries[0].value: want string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Mod{}.Poll(context.Background(), tc.params)
			if err == nil {
				t.Fatal("Poll: want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestRenderCapsRows(t *testing.T) {
	entries := make([]any, 30)
	for i := range entries {
		entries[i] = map[string]any{"key": fmt.Sprintf("k%d", i), "value": "v"}
	}
	params := map[string]any{
		"sections": []any{map[string]any{"title": "big", "entries": entries}},
	}
	data, err := Mod{}.Poll(context.Background(), params)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != maxRows {
		t.Fatalf("len(Rows) = %d, want %d", len(data.Rows), maxRows)
	}
	last := data.Rows[len(data.Rows)-1]
	want := module.Row{Kind: module.RowText, Text: "+12 more", Style: module.StyleDim}
	if !reflect.DeepEqual(last, want) {
		t.Errorf("tail row = %#v, want %#v", last, want)
	}
}
