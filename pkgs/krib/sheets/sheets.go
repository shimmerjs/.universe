// Package sheets loads sheet configs. Repo sheets are authored in CUE and
// committed as exported JSON (the loadable artifact, embedded here); a
// runtime JSON file path is the no-rebuild escape hatch.
package sheets

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shimmerjs/krib/classify"
)

//go:embed *.json
var embedded embed.FS

// Default is the embedded sheet used when no --sheet is given.
const Default = "kitty"

// Load resolves nameOrPath: "" means the embedded default; a value with a
// path separator or a .json suffix is read from disk; anything else is an
// embedded repo sheet name.
func Load(nameOrPath string) (classify.Sheet, error) {
	switch {
	case nameOrPath == "":
		return loadEmbedded(Default)
	case strings.ContainsRune(nameOrPath, '/') || strings.HasSuffix(nameOrPath, ".json"):
		f, err := os.Open(nameOrPath)
		if err != nil {
			return classify.Sheet{}, fmt.Errorf("load sheet: %w", err)
		}
		defer f.Close()
		return Decode(f)
	default:
		return loadEmbedded(nameOrPath)
	}
}

func loadEmbedded(name string) (classify.Sheet, error) {
	b, err := embedded.ReadFile(name + ".json")
	if err != nil {
		return classify.Sheet{}, fmt.Errorf("no embedded sheet %q (have %s)", name, strings.Join(Names(), ", "))
	}
	return Decode(strings.NewReader(string(b)))
}

// Names lists the embedded sheet names.
func Names() []string {
	entries, _ := embedded.ReadDir(".")
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	return out
}

// Decode reads one sheet config as JSON and vets it: unknown fields, unknown
// sorter names, bad regexes, and bad exec vocabulary are all load errors.
func Decode(r io.Reader) (classify.Sheet, error) {
	var s classify.Sheet
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return classify.Sheet{}, fmt.Errorf("decode sheet: %w", err)
	}
	if err := classify.VetSheet(s); err != nil {
		return classify.Sheet{}, err
	}
	return s, nil
}
