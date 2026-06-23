# Authoring reusable workflows

Saved dynamic workflows (`~/.claude/workflows/*.js`) are deployed from this directory via nix. When you write or edit one for me, follow these.

## HOUSE ARG CONVENTION
- Informal `word=value` flags up front: no dashes, space-separated, comma-separated for lists. The prompt is everything from the first token that isn't a known `word=value`.
- Keep to ~4 flags, each with a default and a clamp. Canonical parser: `aw-research.js` -- copy its `parseFlags`/`coerce`.
- Example: `aw-research fanout=8 passes=3 breadth=web,code <question>`.
- Two cross-cutting flags every workflow carries: `intensity=0..10` (one knob that scales the explicit fan-out/vote/pass knobs you did not set; tuned defaults stand when omitted) and `subagents=custom|stock` (`stock` drops the custom agent types so every agent() falls back to the default workflow subagent).

## BUILD VERIFICATION IN -- THE ANTI-PATTERNS THAT KEEP BITING

Each of these makes a *broken* run look like a *clean* run. That's why they matter. Ordered by how often they bite.

- **A cap is not a verdict, and caps must be loud.** Never silent `slice(0, N)`. If you cap verification, `log("verified 7/15, 8 over cap")`, tag the overflow `UNVERIFIED`, and exclude it from the confirmed set. Reason: if synthesis is told to drop only `REFUTED`, an unverified-because-truncated finding launders straight through as confirmed -- worse than never finding it.
- **Never swallow agent failures.** `.catch(() => null)` + `filter(Boolean)` silently deletes a real finding -- or a whole dimension -- so a degraded run is indistinguishable from a clean one. Count the nulls, log them, and return a `{ refuted, unverified, failed, overCap }` tally so I can gate on whether the conclusion rests on holes.
- **Derive scope; never hardcode it.** File lists, line counts (`read in chunks of ~600`), package maps baked into prompt prose rot the instant code moves -- and that is exactly what turns a workflow into a one-off. Phase 0 computes scope from `git diff` / `tree` / glob / a recon agent.
- **Real verification, not single-vote theater.** Load-bearing claims get ≥3 skeptics with an explicit majority/quorum rule, and refute-by-default lives in the *schema* (`isReal: bool`, required), not just the prompt. One verifier that hallucinates a refutation -- or misses a real bug -- is unchecked.
- **Dedup before you verify.** Dedup findings against a `seen` set *before* the verify fan-out, or the same defect found by two lenses gets verified 2-3× and double-listed.
- **Close the loop; emit one verdict.** End on a synthesis stage that reconciles to a single go/no-go; for design/review, feed confirmed findings back into a re-judge until no new confirmed. Never dead-end at N disconnected per-agent JSON blobs that I have to collate by hand.
- **Score honestly.** Don't pick a winner by naive mean -- one CRITICAL from any lens vetoes, regardless of average. Document and test reducer tie rules (`refuted >= confirmed` silently resolves a 1-1 split to REFUTED).
- **`agentType:` must reference a wired agent** (a built-in, or one in `programs.claude-code.agents`). The registry is frozen at session start.

## DESIGN-REVIEW SPECIFICS

A verdict workflow cites stage-prompt partials by their deployed `~/.claude/workflows/partials/` path instead of restating them. Two exist: `SYNTHESIS.md` (the shared verdict spine -- confirmed-only, one reconciled go/no-go, name-what-gets-worse, no option buffet; also read by `aw-review.js`/`aw-audit.js`) and `DESIGN_DOCTRINE.md` (design-specific -- one-recommendation, the rationalization->counter table, spec-as-anchor). The synthesis agent reads them and applies them; don't restate the doctrine in the `.js`. New partials drop in `partials/*.md` and deploy automatically (see `default.nix`).

- **The Stop hook fights "only the final state must be clean."** The go-check Stop hook (build+vet, blocks the turn) hard-gates every turn end, so a workflow that executes a refactor across turns will block on red intermediate `.go` states -- the exact tension the doctrine names. Either land the refactor's execution in a single turn (so the final state is what Stop sees), or make the hook advisory/suppressible for that window. Don't paper over it by contorting the change to stay green intermediate.
- **Fork execution; seed it with the spec, not the thread.** Run the execution stage as a subagent whose only context is the written spec, so design exploration and the stress-test transcript don't pollute it. (`CLAUDE_CODE_SUBAGENT_MODEL=inherit` means per-stage `model:` overrides work, so a stage can also pick its own model -- see the note in `default.nix`.)
