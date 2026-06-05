---
name: go
description: How Go editing is wired on this machine -- the two-phase format+build/vet hooks that run automatically, why LSP is navigation-only, and the one edit-after-format gotcha that breaks your next Edit. Load for ANY Go work: writing, reviewing, refactoring, or debugging Go.
---

# Go in this environment

Go formatting and the build/vet gate are wired as hooks. You do not run `gofmt`, `goimports`, `go build`, or `go vet` by hand to satisfy the "format and build/vet before done" bar -- the hooks do it. Know the contract so you don't fight it.

## THE HOOK CONTRACT

- **Per edit (PostToolUse on Edit/Write/MultiEdit, `*.go`):** `gofmt -e` syntax-gates the file -- a parse error blocks the edit and is surfaced in-turn (basename:line). On clean syntax, `goimports -w` reformats and fixes imports **in place on disk**. The edited path is queued for the Stop pass.
- **Once per turn (Stop):** the queued files are batched by module root (an enclosing `go.work` wins over the nearest `go.mod`), then `go build` runs, and `go vet` runs only if build is clean. Either failing blocks the turn with the real compiler/vet output, scoped to the edited packages. No LSP pass -- the compiler is the gate.

## WHAT THIS MEANS FOR YOU

- **Don't run `gofmt`, `goimports`, `go build`, or `go vet` yourself just to check your work** -- the hooks do it. Run `go build`/`go vet`/`go test` directly only when you need output mid-turn (e.g. iterating on a failing test) before the Stop gate fires.
- **The Stop gate is the ground truth that your edit compiles and vets.** A turn that ends clean means `go build` + `go vet` passed on the edited packages. "Verified done" for Go means the Stop hook came back clean (or you ran build/vet/test yourself); LSP green is not that.

## THE ONE RULE THAT BITES: RE-READ AFTER A GO EDIT

`goimports -w` rewrites the file on disk **after** your edit lands. The on-disk content now differs from what you wrote -- imports reordered/added, formatting normalized. Your next `Edit` builds `old_string` from your mental copy, not disk, so it will miss.

After editing a `.go` file, if the next `Edit`'s `old_string` fails to match, **re-read the file first** -- auto-formatting changed it, the file isn't corrupted. Don't retry the same `old_string`; re-read, then edit against the actual disk content.

## DIAGNOSTICS: COMPILER OVER LSP

LSP/gopls (the `gopls-lsp` plugin) is for **navigation only** -- go-to-definition, find-references, symbols, symbol-aware rename (`gopls rename`). Do not treat its diagnostics as ground truth: they go stale the instant you edit and lag the on-disk state the hooks just rewrote, and they're not what gates the turn. Real errors come from the hooks' `go build`/`go vet`/`go test` output. If the build is clean and LSP still complains, the build wins.

## MODULE CONTEXT GOTCHAS

- Resolve `go doc` / `go build` from the right root: in a `go.work` tree, commands run from the workspace root; otherwise from the package's `go.mod` dir. The Stop hook already scopes this way -- match it when you run tools by hand.
- `no required module provides package X` means run `go mod tidy` (or add the dep), not that the import is wrong.

## WHEN THE GATE IS WRONG, NOT YOU

The Stop gate filters to packages you touched, but a mid-refactor turn can still trip on a genuinely-not-yet-coherent intermediate. That's expected: drive to a clean final state and let the gate pass on the real end state -- don't contort intermediate edits just to keep the gate green every step. Only the final state must build and vet clean.
