// Command gocheck is the Stop hook that gates a turn end on `go build` + `go vet`
// of the .go packages edited this session. It drains the per-session queue the
// gofmt hook writes (/tmp/go-pending-<session>), batches files by module root
// (an enclosing go.work wins over the nearest go.mod), and runs the toolchain.
//
// `go list -e -json` metadata selects the packages `go build` can run on
// (non-empty GoFiles); test-only packages are left to vet. `go vet -json` reports
// findings as JSON on stdout (exit 0 even with findings); compile errors, test
// files included, go to stderr with a non-zero exit.
//
// On any diagnostic it blocks the turn (decision=block) with the full output in
// hookSpecificOutput.additionalContext and keeps the queue so the gate persists;
// a clean run drains it. It always exits 0 -- the gate is the decision field, not
// the exit code. Malformed input exits 0. goBin is pinned by nix at build time.
package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// goBin is the go toolchain path, pinned by nix via ldflags; tests fall back to
// "go" on PATH.
var goBin = "go"

type stopInput struct {
	SessionID      string `json:"session_id"`
	StopHookActive bool   `json:"stop_hook_active"`
}

func main() {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(0)
	}
	var in stopInput
	if err := json.Unmarshal(data, &in); err != nil || in.SessionID == "" {
		os.Exit(0)
	}
	// The go-fmt PostToolUse hook writes the queue to /tmp explicitly; read the
	// same path (not os.TempDir(), which is /var/folders/... on macOS).
	pending := "/tmp/go-pending-" + in.SessionID
	body, err := os.ReadFile(pending)
	if err != nil {
		os.Exit(0) // no queue -> nothing edited this session
	}
	// Re-fire guard: if this Stop already blocked once this cycle, drain and let
	// the turn end (the error was surfaced; do not block forever).
	if in.StopHookActive {
		os.Remove(pending)
		os.Exit(0)
	}

	out := check(groupByRoot(uniqueLines(body)))

	if strings.TrimSpace(out) == "" {
		os.Remove(pending) // clean: drain the queue and pass
		os.Exit(0)
	}

	// failure: hard-gate the turn end, keep the queue so the gate persists.
	first := out
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	block := map[string]any{
		"decision": "block",
		"reason": "go build/vet failed on packages edited this turn -- fix before ending the turn. First error: " +
			first + " (full output in additionalContext).",
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "Stop",
			"additionalContext": out,
		},
	}
	b, err := json.Marshal(block)
	if err != nil {
		os.Exit(0)
	}
	os.Stdout.Write(b)
	os.Exit(0)
}

// check runs build+vet per module root and returns the concatenated diagnostics
// ("" means clean). Roots are processed in sorted order for determinism.
func check(byRoot map[string][]string) string {
	roots := make([]string, 0, len(byRoot))
	for r := range byRoot {
		roots = append(roots, r)
	}
	sort.Strings(roots)

	var out strings.Builder
	for _, root := range roots {
		pkgs := byRoot[root]
		if len(pkgs) == 0 {
			continue
		}
		if d := checkRoot(root, pkgs); d != "" {
			out.WriteString(d)
			if !strings.HasSuffix(d, "\n") {
				out.WriteByte('\n')
			}
		}
	}
	return out.String()
}

// checkRoot builds first (it catches cgo/asm/build-tag breaks a type-check-only
// vet pass can miss); on a build failure, surface it and skip vet for this root.
// Otherwise vet, which also compiles the test files build skips.
func checkRoot(root string, pkgs []string) string {
	buildPkgs := buildablePkgs(root, pkgs)
	if len(buildPkgs) > 0 {
		// build writes errors to stderr and nothing useful to stdout.
		stdout, stderr, _ := run(root, 60*time.Second, append([]string{"build", "-o", os.DevNull}, buildPkgs...)...)
		if bo := strings.TrimSpace(stderr + stdout); bo != "" {
			return bo
		}
	}
	return vet(root, pkgs)
}

// buildablePkgs uses `go list -e -json` metadata to keep only packages with
// non-test Go files -- the ones `go build` can compile. Test-only packages (empty
// GoFiles) are left to vet. If go list yields no parseable metadata and fails
// (e.g. a broken go.mod), fall back to building every package so the error still
// surfaces through build.
func buildablePkgs(root string, pkgs []string) []string {
	stdout, _, code := run(root, 30*time.Second, append([]string{"list", "-e", "-json"}, pkgs...)...)
	return buildableFromList(stdout, code, root, pkgs)
}

// buildableFromList decodes the `go list -e -json` stream and keeps packages with
// non-test GoFiles, falling back to all packages if the listing produced nothing
// and failed.
func buildableFromList(listJSON string, listRC int, root string, pkgs []string) []string {
	type meta struct {
		Dir     string
		GoFiles []string
	}
	var metas []meta
	dec := json.NewDecoder(strings.NewReader(listJSON))
	for dec.More() {
		var m meta
		if dec.Decode(&m) != nil {
			break
		}
		metas = append(metas, m)
	}
	if len(metas) == 0 && listRC != 0 {
		return uniqStrings(pkgs) // total list failure -> build everything
	}
	var build []string
	for _, m := range metas {
		if len(m.GoFiles) == 0 || m.Dir == "" {
			continue
		}
		build = append(build, relPattern(root, m.Dir))
	}
	return uniqStrings(build)
}

type vetDiag struct {
	Posn    string `json:"posn"`
	Message string `json:"message"`
}

// vet runs `go vet -json`: analyzer findings come back as JSON on stdout (with a
// zero exit), genuine compile errors -- including in test files -- on stderr with
// a non-zero exit. Both are surfaced, with file positions made root-relative to
// match the build/stderr diagnostics.
func vet(root string, pkgs []string) string {
	stdout, stderr, _ := run(root, 30*time.Second, append([]string{"vet", "-json"}, pkgs...)...)
	var b strings.Builder
	if s := strings.TrimSpace(stderr); s != "" {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	b.WriteString(parseVet(stdout, root))
	return b.String()
}

// parseVet turns the `go vet -json` object(s) into stable, root-relative lines.
// The payload is map[importpath]map[analyzer][]diag; multiple top-level objects
// (if go emits them per package) are merged. Unparseable non-empty output is
// surfaced verbatim rather than silently dropped.
func parseVet(stdout, root string) string {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" || stdout == "{}" {
		return ""
	}
	merged := map[string]map[string][]vetDiag{}
	dec := json.NewDecoder(strings.NewReader(stdout))
	decoded := false
	for dec.More() {
		var obj map[string]map[string][]vetDiag
		if dec.Decode(&obj) != nil {
			break
		}
		decoded = true
		for pkg, analyzers := range obj {
			if merged[pkg] == nil {
				merged[pkg] = map[string][]vetDiag{}
			}
			for an, ds := range analyzers {
				merged[pkg][an] = append(merged[pkg][an], ds...)
			}
		}
	}
	if !decoded {
		return stdout + "\n"
	}
	var lines []string
	for _, pkg := range sortedKeys(merged) {
		for _, an := range sortedKeys(merged[pkg]) {
			for _, d := range merged[pkg][an] {
				lines = append(lines, relPosn(root, d.Posn)+": "+d.Message+" ("+an+")")
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// findGoRoot walks up from dir: the nearest enclosing go.work wins; otherwise the
// nearest go.mod; otherwise "". Matches the old shell hook's batching rule.
func findGoRoot(dir string) string {
	mod := ""
	for filepath.IsAbs(dir) && dir != string(filepath.Separator) {
		if mod == "" {
			if fi, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !fi.IsDir() {
				mod = dir
			}
		}
		if fi, err := os.Stat(filepath.Join(dir, "go.work")); err == nil && !fi.IsDir() {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return mod
}

// groupByRoot maps each queued, still-existing .go file to its module root and a
// "./rel" package pattern, deduped per root.
func groupByRoot(files []string) map[string][]string {
	byRoot := map[string][]string{}
	for _, f := range files {
		if f == "" {
			continue
		}
		if fi, err := os.Stat(f); err != nil || fi.IsDir() {
			continue
		}
		// Resolve symlinks so root/rel match what `go list` reports for .Dir
		// (always the real path) -- on macOS /tmp and /var are symlinks, so an
		// unresolved root would fail to prefix-strip the resolved package dir.
		if rf, err := filepath.EvalSymlinks(f); err == nil {
			f = rf
		}
		root := findGoRoot(filepath.Dir(f))
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, f)
		if err != nil {
			continue
		}
		byRoot[root] = append(byRoot[root], pkgPattern(filepath.Dir(rel)))
	}
	for r := range byRoot {
		byRoot[r] = uniqStrings(byRoot[r])
	}
	return byRoot
}

func pkgPattern(relDir string) string {
	if relDir == "." || relDir == "" {
		return "."
	}
	return "./" + relDir
}

func relPattern(root, dir string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return dir
	}
	return pkgPattern(rel)
}

// relPosn rewrites a "<file>:<line>:<col>" position to be relative to root.
func relPosn(root, posn string) string {
	parts := strings.Split(posn, ":")
	if len(parts) < 3 {
		return posn
	}
	file := strings.Join(parts[:len(parts)-2], ":")
	if rel, err := filepath.Rel(root, file); err == nil && !strings.HasPrefix(rel, "..") {
		file = rel
	}
	return file + ":" + parts[len(parts)-2] + ":" + parts[len(parts)-1]
}

func uniqueLines(b []byte) []string {
	return uniqStrings(strings.Split(string(b), "\n"))
}

func uniqStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// run executes the go toolchain in dir and returns (stdout, stderr, exitCode).
func run(dir string, timeout time.Duration, args ...string) (string, string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		code = 1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}
	return stdout.String(), stderr.String(), code
}
