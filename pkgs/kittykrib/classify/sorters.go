package classify

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/shimmerjs/kittykrib/envelope"
)

// Sorter stably reorders entries in place. Sorters compose: a chain applies
// left to right, later sorters dominating (all are stable).
type Sorter func([]envelope.Entry)

// DefaultSort is the chain the old cheatsheet applied to every category.
var DefaultSort = []string{"leading-key", "longest-last", "group-next-prev"}

// registry is the closed sorter vocabulary; new sorters are Go changes on
// purpose. Seeded with the four sorters from cheatsheet.go.
var registry = map[string]Sorter{
	"leading-key":     sortByLeadingKey,
	"longest-last":    sortLongestLast,
	"group-next-prev": sortGroupNextPrev,
	"int-keys-last":   sortIntKeysLast,
}

// Sorters resolves a chain of registry names.
func Sorters(names []string) ([]Sorter, error) {
	out := make([]Sorter, len(names))
	for i, n := range names {
		s, ok := registry[n]
		if !ok {
			return nil, fmt.Errorf("unknown sorter %q", n)
		}
		out[i] = s
	}
	return out, nil
}

// leadingToken is the first displayed token of an entry's first chord: its
// first modifier when present, else the key (splitKeys parity).
func leadingToken(e envelope.Entry) string {
	if len(e.Keys) == 0 {
		return ""
	}
	c := e.Keys[0]
	if len(c.Mods) > 0 {
		return strings.ToLower(c.Mods[0])
	}
	return strings.ToLower(c.Key)
}

func sortByLeadingKey(ee []envelope.Entry) {
	slices.SortStableFunc(ee, func(a, b envelope.Entry) int {
		return strings.Compare(leadingToken(a), leadingToken(b))
	})
}

func sortLongestLast(ee []envelope.Entry) {
	slices.SortStableFunc(ee, func(a, b envelope.Entry) int {
		return len(a.Cmd) - len(b.Cmd)
	})
}

func nextPrevBase(cmd string) (string, bool) {
	for _, prefix := range []string{"next_", "previous_"} {
		if base, ok := strings.CutPrefix(cmd, prefix); ok {
			return base, true
		}
	}
	return cmd, false
}

func sortGroupNextPrev(ee []envelope.Entry) {
	slices.SortStableFunc(ee, func(a, b envelope.Entry) int {
		ab, aok := nextPrevBase(a.Cmd)
		bb, bok := nextPrevBase(b.Cmd)
		if aok && bok {
			return strings.Compare(ab, bb)
		}
		if aok {
			return -1
		}
		if bok {
			return 1
		}
		return 0
	})
}

func intKey(e envelope.Entry) (int, bool) {
	for _, c := range e.Keys {
		if n, err := strconv.Atoi(c.Key); err == nil {
			return n, true
		}
	}
	return 0, false
}

func sortIntKeysLast(ee []envelope.Entry) {
	slices.SortStableFunc(ee, func(a, b envelope.Entry) int {
		an, aok := intKey(a)
		bn, bok := intKey(b)
		if !aok && !bok {
			return 0
		}
		if !aok {
			return -1
		}
		if !bok {
			return 1
		}
		return an - bn
	})
}
