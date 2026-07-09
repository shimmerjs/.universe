// krib renders and lists krib sheets from an envelope (or raw kitten JSONL)
// on stdin:
//
//	krib print [--filter name] [--from auto|envelope|kitty-jsonl] [--width n] < sheet
//	krib list [--json] [--from ...] < sheet
//
// print is a static ANSI dump, pager-clean (pipe to less -R); list emits flat
// entries with stable ids for fzf.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shimmerjs/kittykrib/adapter/kittyjsonl"
	"github.com/shimmerjs/kittykrib/chord"
	"github.com/shimmerjs/kittykrib/classify"
	"github.com/shimmerjs/kittykrib/envelope"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "krib:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: krib print|list [flags]")
	}
	switch args[0] {
	case "print":
		return runPrint(args[1:])
	case "list":
		return runList(args[1:])
	default:
		return fmt.Errorf("unknown command %q (want print or list)", args[0])
	}
}

func runPrint(args []string) error {
	fs := flag.NewFlagSet("print", flag.ContinueOnError)
	filter := fs.String("filter", "", "only groups whose name contains this (case-insensitive)")
	from := fs.String("from", "auto", "input format: auto, envelope, kitty-jsonl")
	width := fs.Int("width", 80, "render width in cells")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// F4 parity: the old clod-cheat filter was positional.
	if *filter == "" && fs.NArg() > 0 {
		*filter = fs.Arg(0)
	}

	env, err := decode(*from, os.Stdin)
	if err != nil {
		return err
	}
	groups, err := groupsFor(env)
	if err != nil {
		return err
	}
	groups = filterGroups(groups, *filter)
	out := render(env, groups, *width)
	_, err = io.WriteString(os.Stdout, out)
	return err
}

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit one JSON object per entry")
	from := fs.String("from", "auto", "input format: auto, envelope, kitty-jsonl")
	if err := fs.Parse(args); err != nil {
		return err
	}

	env, err := decode(*from, os.Stdin)
	if err != nil {
		return err
	}
	return writeList(os.Stdout, env, *asJSON)
}

func writeList(w io.Writer, env *envelope.Envelope, asJSON bool) error {
	enc := json.NewEncoder(w)
	for _, en := range env.Entries {
		id := en.ID(env.Kind)
		display := en.Term
		if env.Kind == envelope.KindBindings {
			display = chord.FormatSeq(en.Keys)
		}
		if asJSON {
			if err := enc.Encode(flatEntry{
				ID:      id,
				Display: display,
				Mode:    en.Mode,
				Keys:    keysOrEmpty(en),
				Group:   en.Group,
				Term:    en.Term,
				Body:    en.Body,
				Cmd:     en.Cmd,
			}); err != nil {
				return err
			}
			continue
		}
		detail := en.Cmd
		if strings.TrimSpace(detail) == "" {
			// whitespace-only Cmd is as empty as empty: fall to the Body
			detail = en.Body
		}
		// EVERY column holds the one-line/3-column contract, not just
		// detail: a tab or newline in any field silently shifts columns
		// for the fzf consumer (wave-5 review). Vet rejects control
		// whitespace in names too; this is the defensive layer.
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", col(id), col(display), col(detail)); err != nil {
			return err
		}
	}
	return nil
}

// firstLine is the trimmed first non-empty line of s.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// col fits one value into the tab-separated line contract: first line
// only, interior tabs flattened to spaces.
func col(s string) string {
	return strings.ReplaceAll(firstLine(s), "\t", " ")
}

type flatEntry struct {
	ID      string `json:"id"`
	Display string `json:"display"`
	Mode    string `json:"mode,omitempty"`
	Keys    string `json:"keys,omitempty"`
	Group   string `json:"group,omitempty"`
	Term    string `json:"term,omitempty"`
	Body    string `json:"body,omitempty"`
	Cmd     string `json:"cmd,omitempty"`
}

func keysOrEmpty(en envelope.Entry) string {
	if len(en.Keys) == 0 {
		return ""
	}
	return chord.CanonicalSeq(en.Keys)
}

// decode reads one sheet from r. auto sniffs: a single JSON document carrying
// schemaVersion is an envelope, anything else is tried as kitten JSONL.
func decode(from string, r io.Reader) (*envelope.Envelope, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, fmt.Errorf("empty input: pipe a krib envelope or kitten JSONL to stdin")
	}
	switch from {
	case "envelope":
		return decodeEnvelope(b)
	case "kitty-jsonl":
		return kittyjsonl.Decode(bytes.NewReader(b))
	case "auto":
		var probe struct {
			SchemaVersion *int `json:"schemaVersion"`
		}
		if err := json.Unmarshal(b, &probe); err == nil && probe.SchemaVersion != nil {
			return decodeEnvelope(b)
		}
		return kittyjsonl.Decode(bytes.NewReader(b))
	default:
		return nil, fmt.Errorf("unknown --from %q (want auto, envelope, or kitty-jsonl)", from)
	}
}

func decodeEnvelope(b []byte) (*envelope.Envelope, error) {
	env, warnings, err := envelope.Decode(bytes.NewReader(b))
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "krib: warning:", w)
	}
	return env, err
}

// groupsFor classifies bindings against the built-in kitty sheet and groups
// cards by their data-borne group field.
func groupsFor(env *envelope.Envelope) ([]classify.Grouped, error) {
	if env.Kind == envelope.KindCards {
		return classify.ByGroup(env), nil
	}
	return classify.Classify(classify.KittySheet(), env)
}

func filterGroups(groups []classify.Grouped, filter string) []classify.Grouped {
	if filter == "" {
		return groups
	}
	f := strings.ToLower(filter)
	var out []classify.Grouped
	for _, g := range groups {
		if strings.Contains(strings.ToLower(g.Name), f) {
			out = append(out, g)
		}
	}
	return out
}
