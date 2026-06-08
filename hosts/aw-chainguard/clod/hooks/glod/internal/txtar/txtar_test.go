package txtar

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	got := Parse([]byte("ignored preamble\n-- a --\nhello\nworld\n-- b --\n1"))
	want := map[string]string{"a": "hello\nworld", "b": "1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse = %v, want %v", got, want)
	}
	if len(Parse([]byte("no markers here"))) != 0 {
		t.Error("Parse of marker-less input should be empty")
	}
	// A trailing newline in the archive lands as an empty final line in the last
	// section; callers TrimSpace / JSON-tolerate it (matches the original parsers).
	if g := Parse([]byte("-- x --\nv\n"))["x"]; g != "v\n" {
		t.Errorf("trailing newline: got %q, want %q", g, "v\n")
	}
}
