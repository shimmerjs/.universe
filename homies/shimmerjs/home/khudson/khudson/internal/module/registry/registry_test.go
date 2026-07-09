package registry

import (
	"testing"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"

	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/schema"
)

// TestSchemaModuleVocabularyCompiled pins schema #Module to the registry:
// every name the schema admits must resolve to a compiled module, or a
// vetted config polls a phantom into a repeating error.
func TestSchemaModuleVocabularyCompiled(t *testing.T) {
	mods := All()
	v := cuecontext.New().CompileBytes(schema.Schema, cue.Filename("khudson.cue"))
	if err := v.Err(); err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	def := v.LookupPath(cue.ParsePath("#Module"))
	if err := def.Err(); err != nil {
		t.Fatalf("lookup #Module: %v", err)
	}
	names := disjuncts(t, def)
	if len(names) == 0 {
		t.Fatal("#Module expanded to zero names")
	}
	for _, name := range names {
		if _, ok := mods[name]; !ok {
			t.Errorf("schema #Module %q is not a compiled module", name)
		}
	}
}

// The module.Persistent implementers (cpumem, disk) must be process-wide
// singletons: cmdBus's restoreHist and bus.Run's b.mods each call All() and
// must share the ring-holding instances, or a restored history would land in
// rings nobody polls. Other modules may be fresh per call.
func TestPersistentModulesAreSingletons(t *testing.T) {
	a, b := All(), All()
	found := 0
	for name, m := range a {
		if _, ok := m.(module.Persistent); !ok {
			continue
		}
		found++
		if b[name] != m {
			t.Errorf("%s: Persistent module differs across All() calls; history restore and polling would split", name)
		}
	}
	if found == 0 {
		t.Fatal("no Persistent modules in the registry; the singleton invariant test is vacuous")
	}
}

// disjuncts flattens a disjunction of string literals.
func disjuncts(t *testing.T, v cue.Value) []string {
	t.Helper()
	op, args := v.Expr()
	if op != cue.OrOp {
		s, err := v.String()
		if err != nil {
			t.Fatalf("#Module disjunct is not a string: %v", err)
		}
		return []string{s}
	}
	var out []string
	for _, a := range args {
		out = append(out, disjuncts(t, a)...)
	}
	return out
}
