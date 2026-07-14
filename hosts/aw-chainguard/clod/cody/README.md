# cody

codex (OpenAI's CLI) wired in as clod's second opinion. "cody" is what I call him.

every Claude agent on this machine runs the same pinned model, so a fan-out multiplies eyes but not perspectives -- ten reviewers share one set of blind spots. cody is the different-vendor dissent: the `/codex-consult` skill for one-off consults, and a leg in all six aw-* workflows. finder legs feed the skeptic quorum, search legs run with live web search, and aw-implement only brings him in at review time -- never while code is being written.

## Layout

```
cody/
  default.nix     programs.codex (config.toml, AGENTS.md) + the clod-side wiring
  AGENTS.md       the consultant contract cody operates under
  codex-consult/  the skill that teaches clod when and how to consult him
```

`default.nix` also contributes the codex-consult skill and the `Bash(codex ...)` allowlist entries to `programs.claude-code`. module contributions merge (attrs deep, lists concatenated), so `../default.nix` stays ignorant of everything cody beyond one `imports` line.

the package comes from `../overlays.nix`: codex fresh off nixpkgs master (the `nixpkgs-claude` input), because the server gates new models on CLI version -- a stale codex can't reach the current model at all. that file stays darwin-level rather than living here: home-manager modules can't touch `nixpkgs.overlays` under `useGlobalPkgs`.

## config

`config.toml` pins the flagship at xhigh. the CLI's own default lags model releases (0.142.x still defaulted to gpt-5.5 after 5.6 dropped), so the pin is the point. bump the model line when a new flagship lands; `nix flake update nixpkgs-claude` when the CLI needs to catch up. web search is on by default -- the research legs lean on it.

`AGENTS.md` is the contract: ground every claim in file:line or a source URL, no fabricated symbols, verdict first, ASCII. terms of engagement for a reviewer, not a personality.

## operating notes

- auth is the one mutable dependency. login state lives in `~/.codex/auth.json` and expires outside nix's control. expired auth does not fail clean: ~30s of retry churn, then a bare 401. run `codex login status` before a consult batch.
- xhigh on the flagship spends wall-clock. a 20-minute consult that reads a million tokens of repo is normal operation, not a hang.
- cody's output is a claim, not a verdict. the skill and every workflow leg ground his citations before relaying them; do the same when consulting by hand.
