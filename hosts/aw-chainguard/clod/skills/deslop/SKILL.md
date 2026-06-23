---
name: deslop
description: Strip AI-generated slop from a target before it ships -- narrating comments, model-state leakage (preamble/process-narration/sign-offs), attribution footers, prose filler and booster diction, the decorative-Unicode/emoji/em-dash-cadence residue the ASCII hook misses (chat replies; emoji and cadence in commit/PR bodies), and behavioral over-reach (scope creep, unrequested files, fabricated APIs). A guardrailed scrub pass, not a rewrite. Use when finishing a diff/doc/commit/PR/reply, or when the user runs /deslop. Default target: the current diff plus the reply being written.
---

# Deslop

Slop is the residue that marks output as machine-generated and adds nothing: filler, ceremony, narration, self-reference, hedging, decoration, and over-reach. The bar is output indistinguishable from a sharp engineer who is terse on purpose -- not output that announces an LLM wrote it. This pass ENFORCES the voice/honesty rules already in CLAUDE.md on output that drifted; it does not re-teach them.

It covers the buckets you named -- comments, references/attribution, ASCII, model-state leakage -- plus the angles those miss: behavioral over-reach, fabricated APIs, and inconsistency with the surrounding code.

It is a REVIEW pass over a specified target, never a blanket rewrite. Make text denser and more direct; never pad it toward a general audience or sanitize the register.

## THREE GLOBAL GUARDRAILS (the dangerous part -- read first)

Deslop deletes things. These must SURVIVE the cut:

1. **The user's voice.** Casual, blunt, profane is WELCOME, not slop. "fuck yes that works" / "same bug, other file" stays. Kill the chipper assistant tone (`Perfect!`, `Great question!`), never the authentic register.
2. **Real data.** Non-ASCII that is PAYLOAD -- a real name, parsed file content, a test fixture, a PR literally about Unicode -- stays. Role, not codepoint, decides. Strip decoration, never data.
3. **Load-bearing content.** Earned WHY-comments, necessary defensive code, honest "I didn't run this" caveats, and domain jargon all stay. When unsure, KEEP and flag -- a false strip that deletes a landmine comment or an honest caveat is worse than a surviving tic.

And: **a stylistic pass never changes behavior.** Deleting a comment, booster, or sign-off is safe. Removing a guard, inlining a helper, dropping error-handling, or rewording a commit's technical claim is a CODE change -- propose it separately, do not fold it into deslop.

## CHANNEL MAP (why this skill exists)

The PreToolUse hook blocks decorative Unicode AND banner/divider comments (`// ---- foo ----`, `# ====`) in file content (Write/Edit/MultiEdit), and decorative Unicode in git-commit / gh pr-issue-release authored prose (the Bash matcher). `includeCoAuthoredBy=false` strips the commit Co-authored-by trailer. What the hook still does NOT catch: decorative EMOJI (left unbanned so real-data emoji survive), the ASCII em-dash-CADENCE tic, gratuitous ALL-CAPS emphasis (a regex can't tell it from the user's own deliberate caps headers, so it is deliberately left to judgment here, not hard-blocked), attribution footers, ceremony/structure, and anything in chat replies or built-binary output. That residue is deslop's job:

| Channel | Guarded? | Deslop focus |
|---|---|---|
| File content (Write/Edit/MultiEdit) | ASCII hook | semantic + structural slop; ALL-CAPS emphasis; typography + divider comments already handled |
| `git commit` message (Bash) | ASCII hook + trailer config | emoji, attribution footers, diff-narration, prefix theater (typography + Co-authored-by already handled) |
| `gh pr`/`issue`/`release` body (Bash) | ASCII hook | emoji, footers, template scaffolding, "This PR..." (typography already handled) |
| Chat replies | none | preamble, narration, sign-offs, Unicode, list-ification |
| Built-binary / echoed output | none | emoji/banners baked into log/fmt/echo strings |

## INTEGRITY AND BEHAVIOR (highest harm -- check first)

| Slop tell | Cut to | Keep |
|---|---|---|
| Fabrication: invented func/field/flag/import/config-key/URL asserted without grounding (a non-existent nixpkgs attr, a made-up `go vet` flag, a dead link) | verify against source / `--help` / the repo, or explicitly flag as unverified | a real-but-unfamiliar API -- do NOT "correct" it into a common-looking wrong one |
| Scope creep: diff touches files/symbols outside the ask; unrequested refactor riding along; "I also added X in case" | cut back to the ask; surface a genuine extra as a one-line suggestion, don't ship it | a change mechanically required to make the asked-for change compile/pass |
| Unrequested files: reflexive `README.md` / `SUMMARY.md` / `*.md` report alongside the work | delete; the answer goes in chat, not a file | a file the task actually requires, or one the user asked for |
| Repo-inconsistency: new code clashing with the file's error/log/test idiom, duplicating an existing util, reviving a deliberately dropped dep | match the neighbors; reuse the util; follow the observed commit/PR shape | an intentional, justified departure (fixing a bad local pattern, not perpetuating it) |

## MODEL-STATE LEAK (the "an LLM wrote this" tells)

| Slop tell | Cut to | Keep |
|---|---|---|
| Preamble / flattery / instruction-echo: `Great question`, `Certainly!`, `As requested, here is`, first sentence restating the ask | delete everything before the first load-bearing sentence; lead with the answer | a one-line scoping restatement that disambiguates a genuinely ambiguous ask |
| Process narration: `Let me`, `I'll now`, `First I'll... Now I'll...`, CoT bleed (`Hmm`, `Wait, actually`, `Okay so the user wants`) | just do it / state the result; imperative in commits (`Batch edits by root`, not `I'll batch`) | first person explaining a real decision/tradeoff the user needs |
| Sign-off / recap: `Hope this helps`, `Let me know if`, `In summary`, trailing bullets re-narrating a visible diff | delete; the turn ending IS the offer, the diff shows the work | a TOP-placed TL;DR the user asked for, or a concrete next step that advances the work |
| AI self-reference: `As an AI`, `as of my knowledge cutoff`, self-praise (`Here's a clean implementation`, `following best practices`) | delete | an honest concrete caveat (`didn't run this; no network`) -- name the fact, not the persona |
| Apology / sycophancy: `I apologize`, `You're absolutely right`, `Great catch!` as standalone contrition with no fix | replace with the correction itself, one factual line | a terse real acknowledgement that aids the fix; still admit genuine mistakes |
| Hedge theater: `I think`/`it seems`/`perhaps` on certain facts; `this appears to fix it` after you ran and confirmed it | verify and state flat, or name the specific unknown | calibrated uncertainty the honesty rule DEMANDS (`untested on macOS; run the check`). Erring toward false confidence violates the rule -- the most dangerous one to over-cut |

## COMMENTS AND CODE

| Slop tell | Cut to | Keep |
|---|---|---|
| What-comments: comment paraphrasing the line below; `// Now we...`, `// GetName returns the name`, `// User represents a user` | delete; if the line is unclear, fix the name, don't annotate it | WHY-comments, `//go:` directives, `//nolint`, `// Deprecated:`, godoc carrying real contract (units, nil-behavior, goroutine-safety, error meaning) |
| Leftover deadweight: commented-out code, ownerless `TODO`/`FIXME`, `panic("not implemented")` in a "done" change, stray `_ = x` silencers | delete (git is the history); convert a real TODO to a tracked issue with owner/link | a stub or TODO the user wants as an in-progress marker |
| Dead guards: `if err != nil` after an infallible call, nil-check on a value `make`/`&T{}`'d two lines up, `"this should never happen"` | remove ONLY when provably dead -- this is a behavior change, surface it | untrusted-input / exported-API checks, parsing, syscalls, comma-ok lookups, `recover()` at a goroutine boundary, anything across a trust boundary |
| Noise error-wrapping: `fmt.Errorf("error: %w", err)`, re-wrapping the same err at every layer, bare `except:` swallowing all | wrap once at the boundary with context the caller lacks (path, id, op); else return the bare err | wraps that add real context (`open %s: %w`), sentinel wraps for `errors.Is`/`errors.As` |
| Premature abstraction: interface-for-one-impl, options-bag for 2 params, single-use helper, generics used at one type | inline / drop until the second caller arrives; propose, don't auto-apply on someone's diff | a real second impl, a test fake, a documented module seam, abstraction the user asked for |
| Naming + syntax noise: `userMap`/`nameStr`/Hungarian, `var x int = 0`, `const Zero = 0`, trailing bare `return` | role-named (`users`, `name`, `enabled`), idiomatic inference (`:=`, drop `= 0`/`= ""`) | type-in-name that disambiguates representations (`rawBytes` vs `decoded`), meaningful constants (`MaxRetries=3`), `i/j/k`, receiver letters |
| Tautological tests: asserting the mock returned what it was told; `assertTrue(true)`; names `test1`/`TestHappyPath` | delete; name by behavior pinned; test across a real boundary | table/golden tests, mocking genuine externals (net, clock, fs), characterization tests before a refactor |
| Runtime-output decoration: `=== Starting ===`, emoji status prefixes, `Done!`, banners baked into log/fmt/echo (hook never sees built-binary output) | plain structured lines; let exit codes speak | emoji/CJK/accents in real displayed data, an intentional TUI the user built, structured log lines with diagnostic value |
| Trace logging: log at entry AND exit of every fn, leftover `log.Printf("x is %v")` print-debugging, INFO narration of normal flow | log at boundaries and on error | deliberate telemetry/metrics, a real audit log, debug behind a verbosity flag |

## PROSE

| Slop tell | Cut to | Keep |
|---|---|---|
| Booster / lexical tics: `powerful`, `seamless`, `robust`, `comprehensive`, `leverage`, `utilize`, `delve`; stacked `Furthermore,`/`Notably,`/`Moreover,`; `in today's fast-paced` | plain word or delete (`utilize`->`use`, `leverage`->`use`) | literal-technical uses -- a real test `harness`, a `robust` retry, `leverage` the noun |
| Shouting caps: a plain word ALL-CAPS for emphasis mid-sentence (`do NOT`, `you MUST`, `this ALWAYS fails`), `IMPORTANT:`/`NOTE:`/`WARNING:` admonition labels. The hook deliberately does not block this (can't tell it from real caps), so it lands here. Applies to comments and docs you write, not just chat | lowercase it; let word choice or sentence shape carry the weight (`never edit X`, not `NEVER edit X`); drop the admonition label, just state the thing | acronyms/initialisms (HTTP, JSON, CAS, TUI), SCREAMING_SNAKE consts + env vars, a genuine status token (PASS/FAIL/TODO), the user's own deliberate caps headers and emphasis -- their register stays |
| "Not just X, it's Y" + triads: `not only... but also`, `fast, reliable, and scalable`, every list landing on exactly 3 | drop the negated half; cut triads to the members carrying real content | a genuine contrast the reader needs; a factual list that really has 3 members |
| Filler / throat-clearing: `essentially`, `basically`, `simply`; `it's worth noting that`, `let's dive in`, `with that out of the way` | delete; the sentence survives stronger | `just`/`only` with real scoping force, `typically` honestly quantifying, a load-bearing `because` |
| Over-explanation: defining known terms (`CI (continuous integration)`), `in other words` restatement, explaining a code block then showing it | delete; trust the reader; explain only the non-obvious WHY | a genuinely ambiguous local acronym, a real WHY -- but don't pad correct jargon into a tutorial |
| Markdown ceremony: lockstep `**Term**: gloss` lists, bold on ordinary words, heading + 2-sentence body, `## Overview`/`## Conclusion` | prose when items are few; reserve bullets for parallel scannable items | a real flags/fields reference table, navigation headings in a genuinely long doc |
| Reflexive list-ification: a 2-3 sentence answer chopped into bullets/numbered steps | write the prose | genuinely enumerable parallel items (real steps, options, flags) |
| Templated cadence (low): identical paragraph skeletons, every item opening `Enables...`/`Provides...`, uniform 15-25-word sentences | vary length, merge/split, inject fragments (`Won't fix.`) | deliberate symmetry in a spec or comparison table |

## ARTIFACT CHANNELS (commit / PR / review)

| Slop tell | Cut to | Keep |
|---|---|---|
| Em-dash CADENCE tic + decorative emoji: overused ` -- ` jammed where a comma/period belongs (ASCII, so the hook passes it); gitmoji/emoji section markers (emoji is not in the hook's banned set); any decorative typography in CHAT, which no hook sees | break the cadence -- most em-dashes become periods or commas; strip decorative emoji; ASCII-normalize chat typography | real Unicode payload (a PR about Unicode, quoted data). The hook already blocks decorative TYPOGRAPHY in commit/gh bodies -- don't re-hunt that there |
| Attribution footers: `Generated with Claude Code`, robot-emoji signature, `Co-authored-by: Claude` pasted into a commit BODY (config strips only the trailer) | delete from commit / PR / issue text | a real human co-author or a DCO `Signed-off-by` the user maintains |
| "This PR..." / diff-narration: body opening `This PR/commit/change...`, a bullet inventory mirroring `git show --stat` | state substance directly (`Add X to handle Y`); cut the body unless it carries WHY / constraint / tradeoff / link | a load-bearing body (rationale, repro steps, an upstream link, a migration note) |
| Prefix theater: `feat:`/`fix:`/`chore:` conventional TYPE words where the repo uses `<area>: <subject>` | match `git log --pretty=%s`; the `<area>:` scope is the REAL convention -- drop only the TYPE word | a repo that genuinely uses Conventional Commits across its history |
| Empty PR template: `## Summary`/`## Testing`/`## Checklist` scaffolds with placeholder bodies, unticked ceremonial boxes, `<!-- describe -->` | delete every section/box you aren't filling; a PR body is the few sentences that matter | an ENFORCED `PULL_REQUEST_TEMPLATE.md` whose sections you genuinely complete |
| Review-comment ceremony: a one-line nit wrapped in `**Issue:**`/`**Severity:**`/`**Impact:**`, emoji verdicts, praise sandwich | a sharp human sentence: point at the line, say the problem and the fix | the repo's own /review and /code-review structured-severity output -- sanctioned tooling, not slop |

## GREP THE MECHANICAL ONES

The lexical tells are detectable. Run these against a draft (a chat reply, or a commit/PR body before it posts), then pass every hit through the guardrails -- candidates, not auto-deletes. The Unicode grep is a backstop now: the ASCII hook blocks decorative typography in commit/gh prose, but not the ASCII em-dash cadence, emoji, or anything in chat:

```
# booster diction + filler
rg -i "\b(leverage|utilize|seamless|robust|comprehensive|delve|powerful|in today's|it's worth noting|let's dive|essentially|basically)\b"
# model-state leak
rg -i "\b(great question|certainly|as an ai|i hope this helps|let me know if|as requested|in summary)\b"
# attribution footers
rg -i "generated (with|by)|co-authored-by: *claude|created by claude"
# decorative Unicode that escaped to a commit/PR body (dashes, smart quotes, ellipsis, arrows, bullets, emoji)
rg -nP "[\x{2010}-\x{2027}\x{2030}-\x{205E}\x{2190}-\x{21FF}\x{2600}-\x{27BF}\x{1F000}-\x{1FAFF}]"
```

## PROCEDURE

1. **Identify target + channel.** Channel sets scope: a file (ASCII hook already ran -- skip typography, hunt semantic/structural slop), chat text, a commit message, a gh body, or emitted output. The unguarded channels get the Unicode scan files don't need.
2. **Scope discipline FIRST.** Before any wording, check the change does only what was asked. Flag scope creep and unrequested files before stylistic edits -- biggest wins, and explicitly forbidden by CLAUDE.md.
3. **Scan severity-first.** Integrity/behavior, then the loud model-state / Unicode / what-comment / tautological-test tells, then the cadence and naming nits. Run the greps for the mechanical ones.
4. **Guardrail-check every candidate.** Ask "is this the legit lookalike?" -- real voice, real data, earned WHY, necessary defensive code, load-bearing jargon, intentional structure. Unsure -> KEEP and flag.
5. **Never change behavior in this pass.** Comment/booster/sign-off cuts are safe. Removing a guard, inlining, dropping error-handling, or rewording a technical claim is a code change -- propose separately or confirm.
6. **Verify fabrication suspects** against source / `--help` / the repo, or flag them as unverified. The honesty rule forbids confident assertion of unchecked claims.
7. **Report terse.** What was cut and why, one pass. If the target is a commit/PR about to post, show the cleaned version and let the user ship it. Ask before large structural rewrites or removing anything possibly load-bearing.
