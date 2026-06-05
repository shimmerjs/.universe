---
name: handoff
description: Write a continuity handoff so a fresh session can resume after compaction or a session ends. Use before a long/risky task likely to compact, when the user runs /handoff, or when context is about to be lost. Produces a pointer-index, not a re-summary.
---

# Handoff

A handoff survives compaction by being a **pointer-index**, not a lossy re-summary. It records what the next agent can't reconstruct from the repo: what is *verified* vs merely *assumed*, and the single next action. Everything already on disk or in a tracker is referenced by path/URL, never restated.

## WHERE IT GOES

Write to the OS temp dir, never the workspace -- a handoff is not source and must not land in a diff or get treated as a doc later:

```
"${TMPDIR:-/tmp}/clod-handoff-${session_id}.md"
```

Use the real `session_id` (from the transcript path or the session). A `SessionEnd` hook writes a deterministic git snapshot to this same path as a fallback; your model-authored version is richer and supersedes it. After writing, print the absolute path back so it can be handed to the next session (`claude --resume`, or paste the file).

## RULES

- **Reference, don't duplicate.** Anything captured in a PRD, plan, ADR, issue, commit, or the live diff is cited by path/URL. Do not re-summarize it -- it rots the moment the source moves (same reason we keep pointers out of source comments).
- **VERIFIED vs ASSUMED is mandatory and load-bearing.** Same bar as the `skeptic` agent: a claim is VERIFIED only if you ran it and saw the output. "Builds / passes / works" with no run output is ASSUMED. Never launder an assumption into the verified set -- that is the exact failure the split exists to catch.
- **One next action.** Not a menu. The single thing the next session should do first.
- Keep it terse. A handoff nobody reads is worse than none.

## SCHEMA

```markdown
# Handoff -- <session_id> -- <UTC timestamp>

## Task
<one line: what this session was doing>

## State
- Branch: <branch> @ <short-sha>
- Open files / scope: <paths being edited -- list, don't summarize their contents>
- Diff: `git diff` is the evidence. Note only what isn't obvious from it.

## VERIFIED
<each line: a fact you confirmed by running something. State what you ran.>
- e.g. `nix build .#darwinConfigurations.aw-chainguard.system` -- green
- e.g. `go build ./pkg/signing` -- clean; tests not run

## ASSUMED / UNVERIFIED
<each line: believed-but-not-confirmed. The next session must verify before trusting.>
- e.g. handoff hook fires on SessionEnd -- wired in nix, not yet observed firing

## Next action
<the single first thing to do, concretely>

## Pointers
<paths/URLs only -- specs, the relevant CLAUDE.md, the PR/issue, prior handoff>

## Suggested workflows / skills
<which to load next: a /workflow, the codex-consult skill, etc. -- names only>
```

## WHEN NOT TO HANDOFF

A short, finished, verified task needs no handoff -- the commit and diff are the record. Handoff is for work mid-flight when context is about to be lost.
