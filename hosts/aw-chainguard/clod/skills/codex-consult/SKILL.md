---
name: codex-consult
description: Get a cross-model (codex / GPT-5) second opinion on a diff, design, or hard bug. Use when an independent, different-vendor reviewer is worth more than another Opus pass -- every Claude subagent here runs Opus and shares its blind spots. NOT for routine edits or anything faster to do directly.
---

# codex-consult

A consultant, not an authority. You are the primary agent; codex is a different-model reviewer you can talk to. Form your own view, then present it.

Every subagent in this setup runs Opus (`CLAUDE_CODE_SUBAGENT_MODEL=opus`), so a Claude fan-out cannot give a *true* independent second opinion -- the reviewers share Opus's blind spots. codex (a different vendor) is the only way to get real adversarial diversity. That diversity is the entire reason to reach for this; if you only need more eyes of the same kind, fan out subagents instead.

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

## HOW TO CALL IT

Prefer the MCP `codex` tool if it's wired (`codex` to start a thread, `codex-reply` to continue). The returned `content` is codex's final answer only -- not its reasoning or tool use. Model / effort / sandbox lock at thread start, so if a conversation might get harder, start at the higher effort.

Bash fallback when MCP isn't available or you need a one-shot:

```
# one-shot review of the current diff (cheap, no back-and-forth)
codex review --base main         # or: --uncommitted | --commit <sha>

# read-only consult, output to a file you then read and verify
codex exec -s read-only -a never -o /tmp/codex-result.txt "<one-line intent + question>"
```

Defaults: `-s read-only` (sandbox) and `-a never` (no auto-approval). Do NOT hand codex a workspace-write path on the strength of a prompt instruction -- if it needs to write, that's a real permission decision; surface it to me first. Effort rides on config (`-c model_reasoning_effort=high|xhigh`), there's no dedicated flag. Don't hardcode a model string in your invocation if a profile/default is configured -- let the configured default pick it.

## THREAD HYGIENE

- One line of intent before each call: "New thread -- reviewing the auth refactor in service.go."
- New thread per invocation by default. Continue only when you're directly iterating on the same thing.
- Topic change -> new thread. When in doubt, restart: cheaper than poisoning an ongoing thread with stale context.

## PUSH PAST THE FIRST ANSWER

Treat the first reply as a survey, not the answer. Follow up 2-3 times: press on the edge cases it skipped, the hedges it slipped in, and how its advice applies concretely to *this* code. Stop early only if the first answer is already sharp and you've verified it.
