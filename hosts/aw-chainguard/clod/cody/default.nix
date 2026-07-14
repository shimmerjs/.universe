# Everything cody, one module: the codex CLI (package from the clod
# overlay -- nixpkgs-claude freshness + the pinned version override live in
# ../overlays.nix, the darwin-side companion), its nix-owned config.toml
# and CODEX_HOME/AGENTS.md, the clod-side consult skill, and the Bash
# allowlist slice the skill rides. Imported by ../default.nix; the
# programs.claude-code contributions merge with clod's main block
# (settings is the hm JSON format type: attrs deep-merge, lists
# concatenate).
{ pkgs, ... }:
{
  programs.codex = {
    enable = true;
    package = pkgs.codex;
    # CODEX_HOME/AGENTS.md: the consultant contract (ground every claim,
    # no fabrication, verdict-first, ASCII) -- codex is almost always
    # invoked BY clod as a cross-model leg, so its instructions mirror the
    # house rigor rules rather than restating repo mechanics (per-repo
    # AGENTS.md files layer on top).
    context = ./AGENTS.md;
    # The skill defers model choice to this config ("let the configured
    # default pick it"), so the juiciest model is pinned here -- the CLI's
    # own default lags releases (0.142.x still defaulted to gpt-5.5 after
    # the 5.6 drop), and the server gates new models on CLI version, which
    # the overlay's fresh package covers. Per-call effort escalation stays
    # available via `-c model_reasoning_effort=...`.
    settings = {
      model = "gpt-5.6-sol";
      model_reasoning_effort = "xhigh";
      # live web search default-on: proven on 0.144.x, used by the
      # aw-research/aw-prior-art legs (which no longer need the -c
      # override) and harmless under exec's read-only sandbox elsewhere.
      tools.web_search = true;
    };
  };

  programs.claude-code = {
    skills = {
      # Cross-model second opinion via codex (different vendor, so
      # reviewers don't share the pinned Claude model's blind spots).
      # CLI-only via the Bash(codex ...) perms; threading via `codex exec
      # resume <uuid>`. No MCP server: per-session stdio daemons cost
      # ~32MB each for a rarely-used path, and the hm module's
      # plugin-prefixed tool names broke the allowlist silently.
      codex-consult = ./codex-consult/SKILL.md;
    };
    settings.permissions.allow = [
      # codex-consult: CLI-only, promptless (exec resume carries threads).
      "Bash(codex exec:*)"
      "Bash(codex review:*)"
      "Bash(codex login status:*)"
    ];
  };
}
