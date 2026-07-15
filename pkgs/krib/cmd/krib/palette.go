package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	osexec "os/exec"
	"strings"
	"time"

	"github.com/shimmerjs/krib/classify"
	"github.com/shimmerjs/krib/sheets"
)

// runPalette is the fzf frontend: list the session cache, let fzf pick, then
// execute the accepted entry in-process (no re-scrape, no re-scan). The
// --data cache file is required because the reload binds (show-all toggle)
// re-read it in a subprocess.
func runPalette(args []string) error {
	fs := flag.NewFlagSet("palette", flag.ContinueOnError)
	data := fs.String("data", "", "envelope file (the session cache; required)")
	from := fs.String("from", "auto", "input format: auto, envelope, kitty-jsonl")
	sheetFlag := fs.String("sheet", "", "sheet config: embedded name or JSON file path (default kitty)")
	window := fs.String("window", "", "kitty window id to target (the palette overlay's parent)")
	all := fs.Bool("all", false, "start with the show-all view")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *data == "" {
		return fmt.Errorf("palette requires --data (the session scrape cache)")
	}

	env, err := decodeData(*data, *from)
	if err != nil {
		return err
	}
	sheet, err := sheets.Load(*sheetFlag)
	if err != nil {
		return err
	}
	now := time.Now()
	st, stPath := loadState(env)
	observeAndSave(st, stPath, env, now)
	entries, err := buildList(env, sheet, st, listOpts{all: *all, recentWindow: 14 * 24 * time.Hour, now: now})
	if err != nil {
		return err
	}
	var lines bytes.Buffer
	if err := writeList(&lines, entries, false); err != nil {
		return err
	}

	self, err := os.Executable()
	if err != nil {
		self = "krib"
	}
	fzf := osexec.Command("fzf", paletteArgs(sheet, self, *data, *sheetFlag, *all)...)
	fzf.Stdin = &lines
	var picked bytes.Buffer
	fzf.Stdout = &picked
	fzf.Stderr = os.Stderr
	if err := fzf.Run(); err != nil {
		// 1 = no match, 130 = dismissed (esc/ctrl-c): not errors
		if ee, ok := err.(*osexec.ExitError); ok && (ee.ExitCode() == 1 || ee.ExitCode() == 130) {
			return nil
		}
		return fmt.Errorf("fzf: %w", err)
	}
	id, ok := selectedID(picked.String())
	if !ok {
		return nil
	}
	en, ok := entryByID(env, id)
	if !ok {
		return fmt.Errorf("accepted id %q not in the session cache", id)
	}
	return executeEntry(env, sheet, en, *window, false)
}

// selectedID extracts the hidden id column from fzf's accepted line.
func selectedID(out string) (string, bool) {
	line, _, _ := strings.Cut(strings.TrimRight(out, "\n"), "\n")
	id, _, found := strings.Cut(line, "\t")
	if !found || id == "" {
		return "", false
	}
	return id, true
}

const (
	promptCurated = "krib> "
	promptAll     = "krib all> "
)

// paletteArgs derives the fzf invocation FROM the sheet: group-filter keys
// become alt-<key> binds, and ctrl-a toggles the show-all view by reloading
// the list from the session cache (the prompt doubles as the toggle state).
func paletteArgs(sheet classify.Sheet, self, data, sheetArg string, allDefault bool) []string {
	relist := shq(self) + " list"
	if sheetArg != "" {
		relist += " --sheet " + shq(sheetArg)
	}
	relist += " < " + shq(data)
	prompt := promptCurated
	if allDefault {
		prompt = promptAll
	}
	toggle := fmt.Sprintf(
		`ctrl-a:transform:if [ "$FZF_PROMPT" = %q ]; then echo %q; else echo %q; fi`,
		promptCurated,
		"change-prompt("+promptAll+")+reload("+relist+" --all)",
		"change-prompt("+promptCurated+")+reload("+relist+")",
	)

	args := []string{
		"--ansi",
		"--delimiter=\t",
		"--with-nth=2..",
		"--no-multi",
		"--layout=reverse",
		"--info=inline",
		"--prompt=" + prompt,
		"--bind=" + toggle,
	}
	var hints []string
	for _, g := range sheet.Groups {
		if g.Key == "" {
			continue
		}
		args = append(args, fmt.Sprintf("--bind=alt-%s:change-query('%s )", g.Key, g.Name))
		hints = append(hints, fmt.Sprintf("M-%s %s", g.Key, g.Name))
	}
	header := "C-a toggle all"
	if len(hints) > 0 {
		header += " | " + strings.Join(hints, " | ")
	}
	args = append(args, "--header="+header)
	return args
}

// shq single-quotes s for the $SHELL -c command lines fzf binds execute.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
