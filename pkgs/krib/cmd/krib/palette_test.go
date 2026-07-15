package main

import (
	"strings"
	"testing"
)

// The fzf invocation is derived FROM the sheet: group keys become alt binds,
// the show-all toggle reloads from the session cache, the id column stays
// hidden.
func TestPaletteArgs(t *testing.T) {
	sheet := kittySheet(t)
	args := paletteArgs(sheet, "/bin/krib", "/tmp/cache", "", false)
	joined := strings.Join(args, "\x00")

	for _, want := range []string{
		"--with-nth=2..",
		"--prompt=" + promptCurated,
		"--bind=alt-w:change-query('windows )",
		"--bind=alt-t:change-query('tabs )",
		"--bind=alt-o:change-query('other )",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q:\n%v", want, args)
		}
	}

	var toggle string
	for _, a := range args {
		if strings.HasPrefix(a, "--bind=ctrl-a:transform:") {
			toggle = a
		}
	}
	if toggle == "" {
		t.Fatalf("no show-all toggle bind:\n%v", args)
	}
	for _, want := range []string{
		"reload('/bin/krib' list < '/tmp/cache' --all)",
		"reload('/bin/krib' list < '/tmp/cache')",
		"change-prompt(" + promptAll + ")",
		"change-prompt(" + promptCurated + ")",
	} {
		if !strings.Contains(toggle, want) {
			t.Errorf("toggle missing %q: %q", want, toggle)
		}
	}

	// nix-set default: starting in show-all flips the initial prompt
	allArgs := paletteArgs(sheet, "/bin/krib", "/tmp/cache", "", true)
	if !strings.Contains(strings.Join(allArgs, "\x00"), "--prompt="+promptAll) {
		t.Errorf("all-default args missing the all prompt:\n%v", allArgs)
	}

	// a --sheet override propagates into the reload commands
	sheetArgs := paletteArgs(sheet, "/bin/krib", "/tmp/cache", "/tmp/my sheet.json", false)
	found := false
	for _, a := range sheetArgs {
		if strings.Contains(a, "--sheet '/tmp/my sheet.json'") {
			found = true
		}
	}
	if !found {
		t.Errorf("reload does not carry the sheet override:\n%v", sheetArgs)
	}
}

func TestSelectedID(t *testing.T) {
	id, ok := selectedID("default/cmd+w\t[x] display\tgroups\tdetail\n")
	if !ok || id != "default/cmd+w" {
		t.Fatalf("selectedID = %q, %v", id, ok)
	}
	if _, ok := selectedID(""); ok {
		t.Fatal("empty selection should not resolve")
	}
	if _, ok := selectedID("no-tabs-here\n"); ok {
		t.Fatal("line without columns should not resolve")
	}
}

func TestShq(t *testing.T) {
	if got := shq("/plain/path"); got != "'/plain/path'" {
		t.Fatalf("shq = %q", got)
	}
	if got := shq("a'b"); got != `'a'\''b'` {
		t.Fatalf("shq = %q", got)
	}
}
