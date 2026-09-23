---
name: codex-consult
description: Get a cross-model (codex / GPT) second opinion on a diff, design, or hard bug. Reach for it BEFORE committing an expensive diff, WHEN fanning out same-model review/design/audit subagents or workflows (every Claude agent here runs one pinned model and shares its blind spots), AFTER two failed attempts at the same bug, and on any hard architecture or migration call. NOT for routine edits or anything faster to do directly.
---

# codex-consult

A consultant, not an authority. You are the primary agent; codex is a different-model reviewer you can talk to. Form your own view, then present it.

Every subagent in this setup runs the same Claude model, so a Claude fan-out cannot give a *true* independent second opinion -- the reviewers share that model's blind spots. codex (a different vendor) is the only way to get real adversarial diversity. That diversity is the entire reason to reach for this; if you only need more eyes of the same kind, fan out subagents instead.

## WHEN TO REACH FOR IT

- Reviewing a diff or PR where a missed bug is expensive.
- A hard architecture / migration call where you want a dissenting design.
- A second opinion on a plan before you commit to it.
- A bug you've been stuck on -- a fresh model may see it.

NOT for routine edits, small refactors, or anything faster to do directly. Cross-model review costs latency and money; spend it where independence pays.

## TREAT ITS OUTPUT AS A CLAIM, NOT A VERDICT

This is the global rigor doctrine (`distrust unverified claims -- including subagents'`) applied to a different vendor:

- Check every claim against the actual code. codex may reference symbols, files, or behavior that don't exist -- verify before repeating.
- Compiler / test output outranks anything codex asserts. If it says "this won't build," build it. If it says "the test passes," run it.
- Disagree when warranted. Form your own view *before* presenting to me -- don't launder codex's answer through as your own conclusion.
- When you relay a codex point to me, say it came from codex and say what you verified.

## ALWAYS PASS A PROFILE

`~/.codex/config.toml` is not nix-managed -- it belongs to codex, so that interactive use keeps working. Every nix-owned setting (model, reasoning effort, web search mode, telemetry, skill budget) lives in a profile layer instead, and a profile applies ONLY when you name it. A call without `-p` runs on whatever the account default happens to be, at no pinned effort.

| profile    | model        | effort | web search | use for |
|------------|--------------|--------|------------|---------|
| `review`   | gpt-6-astra  | max    | cached     | diffs, designs, audits, dissent legs |
| `research` | gpt-6-astra  | max    | live       | anything that needs the current web |
| `deep`     | gpt-6-astra  | ultra  | live       | one standalone hard problem |
| `triage`   | gpt-5.6-sol  | low    | cached     | a cheap yes/no sanity check |

`ultra` is max reasoning *plus automatic task delegation* -- codex spawns its own subagents. That is worth it for a standalone consult and wasteful inside a leg that clod has already fanned out, which is why `deep` is its own profile rather than the default.

## HOW TO CALL IT

CLI only (there is no codex MCP server wired; `codex exec resume` carries threads):

```
# one-shot review of the current diff (cheap, no back-and-forth)
codex review --base main         # or: --uncommitted | --commit <sha>

# read-only consult, output to a file you then read and verify
codex exec -p review -s read-only -o /tmp/codex-result.txt "<one-line intent + question>"

# same, but it needs the live web
codex exec -p research -s read-only -o /tmp/codex-result.txt "<question>"

# continue a prior consult: session UUID from the run banner (or --json)
codex exec resume <SESSION_ID> -o /tmp/codex-result.txt "<follow-up>"
```

Put `-p` AFTER the subcommand: `codex exec -p review ...`, never `codex -p review exec ...`. Both parse, but only the first matches the `Bash(codex exec:*)` allowlist entry, and the second prompts.

Default `-s read-only` (sandbox). exec is non-interactive and its approval policy is hard-forced to `never`, so the sandbox is the only guardrail -- keep it read-only. Do NOT hand codex a workspace-write path on the strength of a prompt instruction; if it needs to write, that's a real permission decision, so surface it to me first. Never hardcode a model string -- the profile already pins the strongest model, so an override can only downgrade. Model, effort and sandbox lock at session start, so if a conversation might get harder, start on `deep`.

Resume by explicit UUID only: `--last` picks the newest session across ALL concurrent claude sessions in this cwd (cross-resume race), and `--ephemeral` makes a session unresumable -- don't use either when you might continue.

## AUTH IS THE ONE MUTABLE DEPENDENCY

Everything else is nix-pinned; login state lives in ~/.codex/auth.json and expires outside nix's control. Expired auth does not fail clean: expect ~30s of reconnect churn ending in a bare `401 Unauthorized` with no remediation hint. Don't retry through it -- surface it to me to rerun `codex login`.

`codex doctor` is the one-shot triage: it prints the resolved model, the config.toml parse result, auth state, and MCP/sandbox posture together. `codex login status` is the narrow check if that's all you need. Note `doctor` rejects `-p`, so it reports the BASE config, not a profile.

## THREAD HYGIENE

- One line of intent before each call: "New thread -- reviewing the auth refactor in service.go."
- New thread per invocation by default. Continue only when you're directly iterating on the same thing.
- Topic change -> new thread. When in doubt, restart: cheaper than poisoning an ongoing thread with stale context.

## PUSH PAST THE FIRST ANSWER

Treat the first reply as a survey, not the answer. Follow up 2-3 times: press on the edge cases it skipped, the hedges it slipped in, and how its advice applies concretely to *this* code. Stop early only if the first answer is already sharp and you've verified it.
