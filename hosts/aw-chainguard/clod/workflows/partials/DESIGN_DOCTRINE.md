# Design doctrine -- the design-specific spine for design-review's synthesis

Deployed to `~/.claude/workflows/partials/DESIGN_DOCTRINE.md` and cited by the synthesis stage of `aw-design-review.js`, on top of the shared `SYNTHESIS.md`. NOT a runnable workflow -- it's a stage-prompt partial the synthesis agent reads and applies, the way `aw-research.js` leans on the `researcher`/`skeptic` agents. Lifted from the wolf `architect` skill, adapted to the workflow path (no in-thread human stress-test, no per-skill duplication) and to compiler-as-truth. Cite it by its `~/.claude/...` path from the stage prompt; do not restate it.

## ONE RECOMMENDATION, NAME WHAT GETS WORSE
A design stage emits exactly ONE target, not a menu. State the structure concretely (`pkg/signing/service.go` implementing `Signer` with `Sign`/`Verify`/`Rotate`, not "a service layer"), why it's better, the migration path, and -- required -- **what gets harder**. If you can't name what gets worse, the design isn't thought through; the synthesis stage rejects a recommendation that lists only upside. This kills the option-buffet output and forces trade-off honesty.

## THE SPEC IS THE EXECUTION ANCHOR
Design output is a written spec, not conversation history. Execution re-reads the spec and implements against it. Fork execution into a fresh opus subagent seeded ONLY by the spec. The only valid pause: the spec itself is wrong -- stop, propose a spec amendment, wait for alignment. Do not silently adjust.

## RATIONALIZATION -> COUNTER (THE ANTI-DRIFT TABLE)
When execution feels the urge to deviate, that is a signal to surface the conflict, not to quietly adjust the plan. Each urge has a standing rebuttal:

| Rationalization                                                | Counter                                                                   |
|----------------------------------------------------------------|---------------------------------------------------------------------------|
| "While I'm here, I'll also fix..."                               | Scope creep. Out of spec -> out of scope. Note it, don't do it.            |
| "I'll commit this intermediate step to be safe."               | No safety theater. No per-change commits, no backup files.                |
| "I'll add a compat shim so nothing breaks mid-refactor."       | No shims. Implement the target state directly.                            |
| "Let me build an abstraction for the general case."            | Earn the abstraction with a second real caller, not before.               |
| "The linter/compiler complains about this intermediate state." | Intermediate states need not compile. Only the final state must be clean. |
| "The spec didn't anticipate X, I'll just decide."              | If the spec is wrong, amend it and wait. Don't freelance.                 |
| "This is basically what the spec meant."                       | If it's not what the spec says, it's a spec change. Surface it.           |

## NO LINTER DETOURS -- ONLY THE FINAL STATE MUST BE CLEAN
A sweeping refactor is allowed to leave intermediate `.go` states red. Do not contort the change to keep every step green; verify against the FINAL state.

CONFLICT to respect: the go-check Stop hook (build+vet, blocks the turn) hard-gates on every turn end, so a mid-refactor red state will block. During a known refactor the hook is the thing that fights this principle -- make it advisory/suppressible for that window, or land the refactor in one turn so the final state is what the Stop hook sees. See `../workflows/CLAUDE.md`.

## VERIFICATION IS A GATE, NOT A CLAUSE
"Run tests/builds against the final state" is enforced by the go-check Stop hook, not taken on good faith -- that is the wolf `architect` skill's soft spot and where this setup is ahead. A design-review stage treats a claim of "done/builds/passes" as UNVERIFIED until the compiler/test output backs it, same bar as `skeptic`. LSP is navigation only.
