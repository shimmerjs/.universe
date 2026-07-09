// Package envelope defines the versioned krib interchange payload: one
// envelope per sheet, kind-discriminated entries, groups with metadata.
// All text fields are literal -- no interpolation or escaping grammar exists;
// generated card bodies are display-only.
package envelope

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shimmerjs/kittykrib/chord"
)

// SchemaVersion is the version this library writes. Readers accept N and N-1
// (loudly, via Vet warnings) and fail otherwise.
const SchemaVersion = 1

const (
	KindBindings = "bindings"
	KindCards    = "cards"
	// KindLayout is reserved for the moonlander sheet; no schema exists
	// behind it yet and Vet rejects it.
	KindLayout = "layout"
)

type Envelope struct {
	SchemaVersion int     `json:"schemaVersion"`
	Kind          string  `json:"kind"`
	Meta          Meta    `json:"meta,omitzero"`
	Groups        []Group `json:"groups,omitempty"`
	Entries       []Entry `json:"entries"`
}

type Meta struct {
	Sheet string `json:"sheet,omitempty"`
	// ModAliases maps symbolic modifiers (kitty_mod) to their definitions.
	// Binding keys keep the symbolic form; consumers expand for display.
	ModAliases map[string][]string `json:"modAliases,omitempty"`
	Source     *Source             `json:"source,omitempty"`
}

func (m Meta) IsZero() bool {
	return m.Sheet == "" && len(m.ModAliases) == 0 && m.Source == nil
}

// Source is provenance for staleness UI.
type Source struct {
	Argv []string  `json:"argv,omitempty"`
	At   time.Time `json:"at,omitzero"`
}

// Group carries data-borne group metadata (cards: one group per workflow).
// Bindings sheets usually classify config-side and declare no groups.
type Group struct {
	Name string    `json:"name"`
	Meta GroupMeta `json:"meta,omitzero"`
}

type GroupMeta struct {
	Description string   `json:"description,omitempty"`
	WhenToUse   string   `json:"whenToUse,omitempty"`
	Phases      []string `json:"phases,omitempty"`
}

func (m GroupMeta) IsZero() bool {
	return m.Description == "" && m.WhenToUse == "" && len(m.Phases) == 0
}

// Entry is kind-discriminated: bindings entries carry Keys (+Mode), cards
// entries carry Group+Term (+Body). Cmd is the raw action/command string --
// the classify match target and the fzf payload.
type Entry struct {
	// bindings
	Mode string        `json:"mode,omitempty"`
	Keys []chord.Chord `json:"keys,omitempty"`
	// cards
	Group string `json:"group,omitempty"`
	Term  string `json:"term,omitempty"`
	Body  string `json:"body,omitempty"`
	// shared
	Cmd string `json:"cmd,omitempty"`
}

// ID derives the stable entry identity: bindings are mode+"/"+canonical
// keyseq (empty mode reads as "default"), cards are group+"/"+term.
func (e Entry) ID(kind string) string {
	switch kind {
	case KindCards:
		return e.Group + "/" + e.Term
	default:
		mode := e.Mode
		if mode == "" {
			mode = "default"
		}
		return mode + "/" + chord.CanonicalSeq(e.Keys)
	}
}

// Vet validates the envelope: version skew policy, kind vocabulary, kind vs
// entry shape, chord normalization, group references, and unique entry ids.
// Warnings are non-fatal but must be surfaced (the "loudly" in accept N-1).
func (e *Envelope) Vet() (warnings []string, err error) {
	switch {
	case e.SchemaVersion == SchemaVersion:
	case e.SchemaVersion == SchemaVersion-1 && e.SchemaVersion >= 1:
		warnings = append(warnings, fmt.Sprintf(
			"schemaVersion %d is one behind current %d; regenerate the sheet", e.SchemaVersion, SchemaVersion))
	default:
		return warnings, fmt.Errorf("unsupported schemaVersion %d (this krib speaks %d)", e.SchemaVersion, SchemaVersion)
	}

	if e.Kind != KindBindings && e.Kind != KindCards {
		return warnings, fmt.Errorf("unsupported kind %q", e.Kind)
	}

	groups := make(map[string]bool, len(e.Groups))
	for _, g := range e.Groups {
		if g.Name == "" {
			return warnings, fmt.Errorf("group with empty name")
		}
		if c, bad := badNameChar(g.Name); bad {
			return warnings, fmt.Errorf("group %q must not contain %q", g.Name, c)
		}
		if groups[g.Name] {
			return warnings, fmt.Errorf("duplicate group %q", g.Name)
		}
		groups[g.Name] = true
	}

	ids := make(map[string]bool, len(e.Entries))
	for i, en := range e.Entries {
		if err := vetEntry(e.Kind, en, groups); err != nil {
			return warnings, fmt.Errorf("entry %d: %w", i, err)
		}
		id := en.ID(e.Kind)
		if ids[id] {
			return warnings, fmt.Errorf("duplicate entry id %q", id)
		}
		ids[id] = true
	}
	return warnings, nil
}

// badNameChar reports the first forbidden rune in a name field: "/" (the
// id separator -- unescaped joins would collide distinct names) and
// control whitespace (tabs/newlines shift columns in the one-line list
// contract downstream).
func badNameChar(s string) (string, bool) {
	for _, c := range []string{"/", "\t", "\n", "\r"} {
		if strings.Contains(s, c) {
			return c, true
		}
	}
	return "", false
}

func vetEntry(kind string, en Entry, groups map[string]bool) error {
	switch kind {
	case KindBindings:
		if len(en.Keys) == 0 {
			return fmt.Errorf("bindings entry has no keys")
		}
		if en.Term != "" || en.Body != "" {
			return fmt.Errorf("bindings entry carries cards fields")
		}
		// name fields hold the id + one-line contracts; keys are exempt (a
		// binding on the literal "/" key is legitimate).
		if c, bad := badNameChar(en.Mode); bad {
			return fmt.Errorf("mode %q must not contain %q", en.Mode, c)
		}
		for _, c := range en.Keys {
			if _, err := chord.Normalize(c); err != nil {
				return err
			}
		}
	case KindCards:
		if en.Term == "" {
			return fmt.Errorf("cards entry has no term")
		}
		if en.Group == "" {
			return fmt.Errorf("cards entry has no group")
		}
		if len(en.Keys) > 0 || en.Mode != "" {
			return fmt.Errorf("cards entry carries bindings fields")
		}
		// name fields hold the id + one-line contracts.
		if c, bad := badNameChar(en.Group); bad {
			return fmt.Errorf("group %q must not contain %q", en.Group, c)
		}
		if c, bad := badNameChar(en.Term); bad {
			return fmt.Errorf("term %q must not contain %q", en.Term, c)
		}
	}
	if en.Group != "" && len(groups) > 0 && !groups[en.Group] {
		return fmt.Errorf("entry references undeclared group %q", en.Group)
	}
	return nil
}

// Decode reads and vets one envelope from r.
func Decode(r io.Reader) (*Envelope, []string, error) {
	var e Envelope
	dec := json.NewDecoder(r)
	if err := dec.Decode(&e); err != nil {
		return nil, nil, fmt.Errorf("decode envelope: %w", err)
	}
	warnings, err := e.Vet()
	if err != nil {
		return nil, warnings, err
	}
	return &e, warnings, nil
}
