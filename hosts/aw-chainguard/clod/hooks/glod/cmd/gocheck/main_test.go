package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"glod/internal/txtar"
)

// TestTxtar drives the pure parse/partition functions from testdata/*.txtar,
// whose payloads are real `go list -e -json` and `go vet -json` output captured
// from the toolchain (see each fixture's header). Each archive carries a `kind`:
//
//	kind=vet:       vet_json, root            -> parseVet, compared to want
//	kind=buildable: list_json, list_rc, root, pkgs -> buildableFromList vs want
func TestTxtar(t *testing.T) {
	paths, err := filepath.Glob("testdata/*.txtar")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no testdata/*.txtar fixtures found")
	}
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".txtar")
		t.Run(name, func(t *testing.T) {
			f := txtar.Parse(mustRead(t, p))
			switch strings.TrimSpace(f["kind"]) {
			case "vet":
				got := strings.TrimRight(parseVet(f["vet_json"], strings.TrimSpace(f["root"])), "\n")
				want := strings.TrimRight(f["want"], "\n")
				if got != want {
					t.Errorf("parseVet mismatch\n got: %q\nwant: %q", got, want)
				}
			case "buildable":
				rc, _ := strconv.Atoi(strings.TrimSpace(f["list_rc"]))
				got := buildableFromList(f["list_json"], rc, strings.TrimSpace(f["root"]), strings.Fields(f["pkgs"]))
				want := strings.Fields(f["want"])
				sort.Strings(want)
				if want == nil {
					want = []string{}
				}
				if got == nil {
					got = []string{}
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("buildableFromList mismatch\n got: %v\nwant: %v", got, want)
				}
			default:
				t.Fatalf("%s: unknown kind %q", p, f["kind"])
			}
		})
	}
}

func TestRelPosn(t *testing.T) {
	cases := []struct{ root, posn, want string }{
		{"/r", "/r/a/b.go:3:5", "a/b.go:3:5"},
		{"/r", "/r/main.go:1:1", "main.go:1:1"},
		{"/r", "/other/x.go:2:2", "/other/x.go:2:2"}, // outside root: left absolute
		{"/r", "no-colons", "no-colons"},
	}
	for _, c := range cases {
		if got := relPosn(c.root, c.posn); got != c.want {
			t.Errorf("relPosn(%q,%q)=%q want %q", c.root, c.posn, got, c.want)
		}
	}
}

func TestPkgPattern(t *testing.T) {
	for in, want := range map[string]string{".": ".", "": ".", "a/b": "./a/b", "x": "./x"} {
		if got := pkgPattern(in); got != want {
			t.Errorf("pkgPattern(%q)=%q want %q", in, got, want)
		}
	}
}

func TestFindGoRoot(t *testing.T) {
	tmp := t.TempDir()
	// plain module: tmp/m/go.mod, query from tmp/m/pkg -> tmp/m
	mod := filepath.Join(tmp, "m")
	mkdirs(t, filepath.Join(mod, "pkg"))
	touch(t, filepath.Join(mod, "go.mod"))
	if got := findGoRoot(filepath.Join(mod, "pkg")); got != mod {
		t.Errorf("plain module: got %q want %q", got, mod)
	}
	// go.work wins over a nested go.mod: tmp/w/go.work, tmp/w/m/go.mod
	work := filepath.Join(tmp, "w")
	inner := filepath.Join(work, "m")
	mkdirs(t, filepath.Join(inner, "pkg"))
	touch(t, filepath.Join(work, "go.work"))
	touch(t, filepath.Join(inner, "go.mod"))
	if got := findGoRoot(filepath.Join(inner, "pkg")); got != work {
		t.Errorf("go.work should win: got %q want %q", got, work)
	}
	// bare tree: nothing we created has go.mod/go.work, so findGoRoot must not
	// return a path inside our tmp tree (a go.mod above tmp is fine).
	bare := filepath.Join(tmp, "bare", "deep")
	mkdirs(t, bare)
	if got := findGoRoot(bare); strings.HasPrefix(got, tmp) {
		t.Errorf("bare tree: got %q, inside the test tree", got)
	}
}

func TestGroupByRoot(t *testing.T) {
	tmp := t.TempDir()
	mod := filepath.Join(tmp, "m")
	mkdirs(t, filepath.Join(mod, "sub"))
	touch(t, filepath.Join(mod, "go.mod"))
	root := filepath.Join(mod, "root.go")
	sub := filepath.Join(mod, "sub", "s.go")
	touch(t, root)
	touch(t, sub)
	got := groupByRoot([]string{root, sub, sub, "", "/does/not/exist.go"})
	rmod, err := filepath.EvalSymlinks(mod)
	if err != nil {
		rmod = mod
	}
	want := map[string][]string{rmod: {".", "./sub"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("groupByRoot\n got: %v\nwant: %v", got, want)
	}
}

// TestIntegrationCheck exercises the real toolchain end to end; notably a clean
// test-only package must not trip the build gate. Gated behind
// CLOD_GOCHECK_INTEGRATION (it skips in the nix build, which avoids nested go runs).
func TestIntegrationCheck(t *testing.T) {
	if os.Getenv("CLOD_GOCHECK_INTEGRATION") == "" {
		t.Skip("set CLOD_GOCHECK_INTEGRATION=1 to run (needs the go toolchain)")
	}
	if _, err := exec.LookPath(goBin); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/it\n\ngo 1.22\n")

	files := map[string]string{
		"lib/lib.go":          "package lib\nfunc Add(a, b int) int { return a + b }\n",
		"testonly/x_test.go":  "package testonly\nimport \"testing\"\nfunc TestX(t *testing.T){ _ = t }\n",
		"vetbug/v.go":         "package vetbug\nimport \"fmt\"\nfunc Bad(){ fmt.Printf(\"%d\", \"x\") }\n",
		"broken/b.go":         "package broken\nfunc Bad() int { return \"nope\" }\n",
		"testbreak/t.go":      "package testbreak\nfunc O() int { return 1 }\n",
		"testbreak/t_test.go": "package testbreak\nimport \"testing\"\nfunc TestO(t *testing.T){ var x int = \"no\"; _ = x; _ = t }\n",
	}
	for rel, src := range files {
		write(t, filepath.Join(root, rel), src)
	}
	abs := func(rel string) string { return filepath.Join(root, rel) }

	cases := []struct {
		name     string
		file     string
		wantPass bool
		needle   string
	}{
		{"clean lib", "lib/lib.go", true, ""},
		{"clean test-only pkg (the regression)", "testonly/x_test.go", true, ""},
		{"compile error in non-test file", "broken/b.go", false, "cannot use"},
		{"vet printf finding", "vetbug/v.go", false, "Printf"},
		{"compile error in a test file only", "testbreak/t_test.go", false, "cannot use"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := check(groupByRoot([]string{abs(c.file)}))
			pass := strings.TrimSpace(out) == ""
			if pass != c.wantPass {
				t.Fatalf("pass=%v want %v; output=%q", pass, c.wantPass, out)
			}
			if c.needle != "" && !strings.Contains(out, c.needle) {
				t.Errorf("output %q missing %q", out, c.needle)
			}
		})
	}
}

// --- helpers ---

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mkdirs(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, p string) {
	t.Helper()
	mkdirs(t, filepath.Dir(p))
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, content string) {
	t.Helper()
	mkdirs(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
