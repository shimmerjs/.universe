---
name: handoff
description: Write a continuity handoff so a fresh session can resume after compaction or a session ends. Use before a long/risky task likely to compact, when the user runs /handoff, or when context is about to be lost. Produces a pointer-index, not a re-summary.
---

# Handoff

A handoff survives compaction by being a **pointer-index**, not a lossy re-summary. It records what the next agent can't reconstruct from the repo: what is *verified* vs merely *assumed*, and the single next action. Everything already on disk or in a tracker is referenced by path/URL -- restated only as far as the orientation rule below allows.

## WHERE IT GOES

Write to the work-docs home, never inside the workspace -- a handoff is not source and must not land in a diff. For a worktree-layout repo (worktrees as siblings under one root), that is the `docs/` dir next to the worktrees:

```
<worktree_root>/docs/handoffs/clod-handoff-<YYYY-MM-DD>-<slug>.md
```

`<slug>` is the arc in 2-4 kebab words (`txtar-complete`, `unhardcode-apk-arch`) -- filenames are for humans scanning the dir and for "prior handoff" chains, so no session ids in them; the session id lives in the doc header. Same arc, same day: update the existing file in place -- a handoff is the arc's continuity doc, not a journal.

No worktree setup: fall back to the OS temp dir, `"${TMPDIR:-/tmp}/clod-handoff-<YYYY-MM-DD>-<slug>.md"`. **Temp handoffs are mortal**: /tmp is cleared at reboot and $TMPDIR (/var/folders) is reaped after ~3 idle days. Fine for same-day compaction continuity; if resumption may be days out, say so when printing the path so the owner can copy the file somewhere durable.

After writing, print the absolute path back so it can be handed to the next session (`claude --resume`, or paste the file).

## RULES

- **Reference, don't duplicate -- but do orient.** Anything captured in a PRD, plan, ADR, issue, commit, or the live diff is cited by path/URL, not re-summarized (it rots the moment the source moves). Narrow exception: when the pointer alone isn't navigable in a couple of minutes (a 150-file amended commit, a generated tree), the State section carries an anchor map -- the load-bearing paths, one clause each. Orientation, not narration; if a fresh session would find it from the pointers alone, don't restate it.
- **VERIFIED vs ASSUMED is mandatory and load-bearing.** Same bar as the `skeptic` agent: a claim is VERIFIED only if you ran it and saw the output. State what you ran. "Builds / passes / works" with no run output is ASSUMED. Never launder an assumption into the verified set -- that is the exact failure the split exists to catch.
- **One next action.** Not a menu of things the next session might try. When the arc is blocked on an owner decision, the next action IS getting that decision: mark it BLOCKED and record the pending question and its options verbatim -- that is still one action, not a menu.
- **Durable lessons go to memory, not the handoff.** Handoffs get pruned; a gotcha that outlives the arc (tool trap, platform behavior, non-hermetic test) belongs in a memory file. The Gotchas section is only for arc-scoped traps the next session would re-derive expensively, and should point at any memory it wrote instead of restating it.
- **If you're writing phases, you're writing a plan.** Step ladders and verification sequences are a devplan: give them their own work doc and point at it. The handoff stays the index.
- Keep it terse. A handoff nobody reads is worse than none.

## SCHEMA

Optional sections are omitted when empty -- don't pad.

```markdown
# Handoff -- <slug> -- <session_id> -- <UTC timestamp>

## Task
<one line: what this session was doing>

## State
- Branch: <branch> @ <short-sha>
- Scope / anchor map: <paths being edited; when the diff pointer alone isn't
  navigable, one clause per load-bearing path -- never prose summaries>
- Diff: `git diff` is the evidence. Note only what isn't obvious from it.
- Docs/ledgers already updated this session: <paths>   (optional line)

## VERIFIED
<each line: a fact you confirmed by running something. State what you ran.>
- e.g. `nix build .#darwinConfigurations.aw-chainguard.system` -- green
- e.g. `go build ./pkg/signing` -- clean; tests not run

## ASSUMED / UNVERIFIED
<each line: believed-but-not-confirmed. The next session must verify before trusting.>
- e.g. `nix flake check` is green -- assumed from a local build, not run

## Next action
<the single first thing to do, concretely. If blocked on the owner:
BLOCKED -- <the pending question and its options, verbatim>>

## Gotchas   (optional)
<arc-scoped traps discovered this session that a fresh session would
re-derive expensively; durable ones went to memory -- point at those>

## Pointers
<paths/URLs only -- specs, the relevant CLAUDE.md, the PR/issue, the devplan
if one exists, prior handoffs in the chain, memory files written>

## Suggested workflows / skills
<which to load next: a /workflow, the codex-consult skill, etc. -- names only>
```

## WHEN NOT TO HANDOFF

A short, finished, verified task needs no handoff -- the commit and diff are the record. Handoff is for work mid-flight when context is about to be lost.
