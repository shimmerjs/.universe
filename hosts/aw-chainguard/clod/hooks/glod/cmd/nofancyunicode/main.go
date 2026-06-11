// Command nofancyunicode is a PreToolUse(Write|Edit|MultiEdit|Bash) guard: it
// blocks decorative Unicode in content clod writes, ASCII only.
//
// For file writes it inspects only the new text (content/new_string), so
// pre-existing characters are never flagged. For Bash it scans git-commit and gh
// pr/issue/release authored prose -- the commit-message and PR/issue-body
// channels a file guard never sees.
//
// Exit 2 + stderr denies the tool call and shows the message back, forcing an
// ASCII redo. Malformed input exits 0. Only decorative typography is banned;
// real-data accents, CJK, and emoji pass through. Statusline sources are
// exempt entirely: their glyphs are rendered UI data, not prose decoration.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// banned maps each decorative codepoint to the ASCII it should be. Keys are hex
// code points, not glyphs, so this source stays ASCII.
var banned = buildBanned()

func buildBanned() map[rune]string {
	pairs := []struct {
		r    rune
		repl string
	}{
		{0x2014, "-- or -"},        // em dash
		{0x2013, "-"},              // en dash
		{0x2026, "..."},            // ellipsis
		{0x2018, "'"},              // left single quote
		{0x2019, "'"},              // right single quote
		{0x201C, "\""},             // left double quote
		{0x201D, "\""},             // right double quote
		{0x2192, "->"},             // rightwards arrow
		{0x2190, "<-"},             // leftwards arrow
		{0x21D2, "=>"},             // rightwards double arrow
		{0x21D0, "<="},             // leftwards double arrow
		{0x2022, "-"},              // bullet
		{0x25B8, ">"},              // black right-pointing small triangle
		{0x25BA, ">"},              // black right-pointing pointer
		{0x25AA, "-"},              // black small square
		{0x2713, "[x]"},            // check mark
		{0x2714, "[x]"},            // heavy check mark
		{0x2717, "[ ]"},            // ballot x
		{0x2718, "[ ]"},            // heavy ballot x
		{0x26A0, "!"},              // warning sign
		{0x00A0, "a normal space"}, // non-breaking space
	}
	m := make(map[rune]string, len(pairs))
	for _, p := range pairs {
		m[p.r] = p.repl
	}
	return m
}

// proseCmd matches the git/gh subcommands that carry model-authored prose:
// commit messages and PR/issue/release titles, bodies, and comments. Read-only
// git/gh calls carry no authored text, so they are never scanned.
var proseCmd = regexp.MustCompile(`\bgit\s+commit\b|\bgh\s+(?:pr|issue|release)\s+(?:create|edit|comment|review)\b`)

// glyphSource matches file paths whose glyphs are rendered UI, not prose
// decoration -- the statusline renderers maintained in ~/.universe draw status
// marks and sparklines. Statusline-named files elsewhere are not exempt.
var glyphSource = regexp.MustCompile(`(?i)/\.universe/.*statusline`)

type hookInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		FilePath  string `json:"file_path"`
		Content   string `json:"content"`
		NewString string `json:"new_string"`
		Command   string `json:"command"`
		Edits     []struct {
			NewString string `json:"new_string"`
		} `json:"edits"`
	} `json:"tool_input"`
}

// decide runs the guard against a raw PreToolUse stdin payload. It returns the
// process exit code (2 = deny) and the stderr message to print (empty when the
// call is allowed). It never errors: malformed or irrelevant input -> allow.
func decide(stdin []byte) (code int, stderr string) {
	var in hookInput
	if err := json.Unmarshal(stdin, &in); err != nil {
		return 0, ""
	}

	if in.ToolName != "Bash" && glyphSource.MatchString(in.ToolInput.FilePath) {
		return 0, ""
	}

	var chunks []string
	switch in.ToolName {
	case "Write":
		chunks = append(chunks, in.ToolInput.Content)
	case "Edit":
		chunks = append(chunks, in.ToolInput.NewString)
	case "MultiEdit":
		for _, e := range in.ToolInput.Edits {
			chunks = append(chunks, e.NewString)
		}
	case "Bash":
		// Only the authored-prose subcommands; everything else carries no text
		// the model wrote as content.
		if !proseCmd.MatchString(in.ToolInput.Command) {
			return 0, ""
		}
		chunks = append(chunks, in.ToolInput.Command)
	default:
		return 0, ""
	}

	text := strings.Join(chunks, "\n")
	if text == "" {
		return 0, ""
	}

	// Collect each banned rune once, in order of first appearance, for stable
	// output.
	var found []rune
	seen := make(map[rune]bool)
	for _, r := range text {
		if _, ok := banned[r]; ok && !seen[r] {
			seen[r] = true
			found = append(found, r)
		}
	}
	if len(found) == 0 {
		return 0, ""
	}

	where := "files you write"
	if in.ToolName == "Bash" {
		where = "commit messages and PR/issue bodies"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "clod: ASCII only in %s. Decorative Unicode - replace and retry:\n", where)
	for _, r := range found {
		fmt.Fprintf(&b, "  U+%04X %q -> use %s\n", r, string(r), banned[r])
	}
	return 2, b.String()
}

func main() {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(0)
	}
	code, stderr := decide(data)
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	os.Exit(code)
}
