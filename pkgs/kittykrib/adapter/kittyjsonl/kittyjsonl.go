// Package kittyjsonl adapts the CURRENT kits/keybindings.py kitten output --
// a {"kitty_mod": ...} pseudo-record followed by {mode, keys, action} JSONL
// lines -- into a krib bindings envelope. The kitten stays frozen; format
// changes land here.
package kittyjsonl

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/shimmerjs/kittykrib/chord"
	"github.com/shimmerjs/kittykrib/envelope"
)

// Decode parses kitten JSONL into a vetted bindings envelope. Vet warnings
// cannot occur here (the adapter writes the current schema version), so only
// the envelope and an error are returned.
func Decode(r io.Reader) (*envelope.Envelope, error) {
	env := &envelope.Envelope{
		SchemaVersion: envelope.SchemaVersion,
		Kind:          envelope.KindBindings,
		Meta:          envelope.Meta{Sheet: "kitty"},
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var rec map[string]string
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if km, ok := rec["kitty_mod"]; ok {
			mods, err := chord.ParseMods(km)
			if err != nil {
				return nil, fmt.Errorf("line %d: kitty_mod: %w", line, err)
			}
			if env.Meta.ModAliases == nil {
				env.Meta.ModAliases = map[string][]string{}
			}
			env.Meta.ModAliases["kitty_mod"] = mods
			continue
		}
		keys, err := chord.ParseSpec(rec["keys"])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		env.Entries = append(env.Entries, envelope.Entry{
			Mode: rec["mode"],
			Keys: keys,
			Cmd:  rec["action"],
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(env.Entries) == 0 {
		return nil, fmt.Errorf("no bindings in input")
	}
	if _, err := env.Vet(); err != nil {
		return nil, err
	}
	return env, nil
}
