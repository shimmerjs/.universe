---
name: memory-prune
description: Audit the persistent memory (MEMORY.md index + per-fact files) and prune what no longer earns its place. Use when asked to prune/clean/trim memory, or proactively when the index has accreted cruft (e.g. before a handoff).
---

# Memory prune

Memory is for durable facts a fresh session can't reconstruct from the repo: who the user is, how they want you to work, ongoing project constraints, external pointers. Everything else rots. This pass removes the rot and leaves the load-bearing facts denser, so the index that loads every session stays signal.

It ENFORCES the memory rules already in CLAUDE.md (record durable facts only; don't duplicate what the repo records; prune one-off work). It does not re-teach them.

## THE ONE GUARDRAIL (read first)

Pruning deletes things. A false delete loses a fact no session can rebuild; a surviving stale note is cheap. So: **when unsure, KEEP and flag.** Memories are point-in-time -- a file:line or "current work" claim may have moved. Before deleting one as wrong, verify against the current repo; before deleting one as stale, confirm the work it describes actually shipped. Never delete a `user`/`feedback` preference just because it's old -- those don't expire.

## WHAT TO PRUNE

- **One-off / intermediate work.** Notes about a task that's done and merged, a debugging detour, a "currently doing X" that's no longer current. Git and the diff are the record; memory is not a worklog.
- **Superseded.** A fact a newer memory or a code change replaces. Keep the current one; drop the stale.
- **Duplicates the repo.** Anything now captured in CLAUDE.md, a README, code structure, a comment, a tracked doc, or git history. If the repo says it, memory shouldn't.
- **Wrong / outdated.** A claim that no longer matches the code (verify first). A `project` goal that's been abandoned or completed.
- **Resolved pre-existing-breakage notes** once the breakage is fixed (e.g. a "X fails to eval, baseline before blaming your change" note after X is fixed).

## WHAT TO KEEP

- `user` -- who they are, durable preferences and expertise. Doesn't expire.
- `feedback` -- corrections and confirmed working approaches, with the why. The whole point is they persist across sessions.
- `project` -- live goals/constraints not derivable from the code. Keep while live; prune when shipped/abandoned.
- `reference` -- external pointers (URLs, dashboards, tickets) that still resolve.
- Anything genuinely non-obvious and not in the repo. When unsure, KEEP.

## PROCEDURE

1. **Read the index + every file it points to.** `MEMORY.md` is the loaded-each-session index (one line per memory). Read it, then read each linked file's body + frontmatter `type`.
2. **Classify each: keep / prune / update.** Apply WHAT TO PRUNE vs WHAT TO KEEP. For `update`, the fact is still real but stale or bloated -- tighten it, don't delete.
3. **Verify before deleting a fact as wrong or done.** Check the claim against the current repo / git; confirm "done" work actually shipped. Honesty rule: don't delete on an assumption.
4. **Apply.** Delete the file AND its `MEMORY.md` line together (a dangling index line or an unindexed file is its own rot). Rewrite the index line for any updated memory. Fix `[[links]]` that now point at a deleted memory.
5. **Report terse.** What was pruned/updated and why, one pass. List kept-but-flagged items so the user can decide. Ask before deleting anything possibly load-bearing.
