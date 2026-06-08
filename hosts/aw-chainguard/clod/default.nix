{ lib, pkgs, ... }:
let
  # writeShellApplication-wrapped hook programs (nix-pinned binaries), defined in
  # ./hooks/default.nix alongside the scripts they wrap.
  hooks = import ./hooks { inherit pkgs; };
in
{
  programs.claude-code = {
    enable = true;
    context = ./SYSTEM_PROMPT.md;
    agents = {
      researcher = ./agents/researcher.md;
      skeptic = ./agents/skeptic.md;
      reviewer = ./agents/reviewer.md;
      designer = ./agents/designer.md;
      mapper = ./agents/mapper.md;
    };
    # codex-consult skill lives at ./skills/codex-consult/SKILL.md but is NOT wired
    # yet - codex isn't configured. To enable: add the `codex` skill line back here,
    # plus a `codex` MCP server (or Bash(codex ...) perms) so the calls don't prompt.
    skills = {
      # Cross-session/compaction continuity. Model-authored handoff with a fixed
      # schema (VERIFIED vs ASSUMED, next action), written to the OS temp dir and
      # referencing artifacts by path. The SessionEnd hook below writes a
      # deterministic git fallback to the same path.
      handoff = ./skills/handoff/SKILL.md;
      # Go tooling contract: documents the two-phase format/build/vet hooks wired
      # above, gates LSP to navigation-only, and carries the re-read-after-format
      # gotcha. Auto-loads on Go work via its description (no SYSTEM_PROMPT
      # dispatcher line) — keeps the machine-specific Go mechanics out of the lean
      # universal prompt.
      go = ./skills/go/SKILL.md;
      # Deslop: a final scrub pass that strips AI-generated residue -- narrating
      # comments, model-state leakage, attribution footers, prose filler, the
      # decorative Unicode that escapes the file-write ASCII hook through the
      # chat/commit/gh channels it never sees, and behavioral over-reach (scope
      # creep, unrequested files, fabricated APIs). Guardrail-driven so it never
      # strips real voice, real data, earned comments, or mutates behavior.
      deslop = ./skills/deslop/SKILL.md;
    };
    mcpServers = {
      linear = {
        type = "http";
        url = "https://mcp.linear.app/mcp";
      };
    };
    settings = {
      apiKeyHelper = "/usr/bin/security find-generic-password -s anthropic-api-key -w";
      model = "opus";
      effortLevel = "xhigh";
      autoScrollEnabled = false;
      statusLine = {
        type = "command";
        command = "~/.claude/statusline.sh";
        # Renders instantly from cache; lower interval keeps the spend ledger and
        # the live agent/workflow row fresh while the session is idle (e.g. a
        # background workflow running while you read).
        refreshInterval = 5;
      };
      # Rich per-agent rows in the subagent panel during fan-out / workflows.
      subagentStatusLine = {
        type = "command";
        command = "~/.claude/subagent-statusline.sh";
      };
      # Go hooks: format/syntax-gate on edit, build+vet gate on stop. Binaries are
      # nix-pinned (writeShellApplication runtimeInputs), so they never depend on PATH.
      hooks = {
        # ASCII guard: deny decorative Unicode in anything clod writes, every
        # project. Bash is matched too so it also covers git-commit messages and
        # gh pr/issue/release bodies -- prose channels that bypass the file-write
        # matcher (the hook self-filters to those subcommands; ~26ms/Bash call,
        # almost all of it python startup).
        PreToolUse = [
          {
            matcher = "Write|Edit|MultiEdit|Bash";
            hooks = [
              {
                type = "command";
                command = "${hooks.glod}/bin/nofancyunicode";
              }
            ];
          }
        ];
        PostToolUse = [
          {
            matcher = "Edit|Write|MultiEdit";
            hooks = [
              {
                type = "command";
                command = "${hooks.goFmtHook}/bin/clod-go-fmt-hook";
              }
            ];
          }
          {
            matcher = "Bash";
            hooks = [
              {
                type = "command";
                command = "${hooks.goBuildSweep}/bin/clod-go-build-sweep";
              }
            ];
          }
        ];
        Stop = [
          {
            hooks = [
              {
                type = "command";
                command = "${hooks.glod}/bin/gocheck";
              }
            ];
          }
        ];
        # SessionEnd: drop a git breadcrumb for continuity. Not PreCompact — that
        # event is block-only, can't add context, and fires repeatedly on auto-compact.
        SessionEnd = [
          {
            hooks = [
              {
                type = "command";
                command = "${hooks.handoffSnapshotHook}/bin/clod-handoff-snapshot-hook";
              }
            ];
          }
        ];
      };
      includeCoAuthoredBy = false;
      enabledPlugins = {
        "gopls-lsp@claude-plugins-official" = true;
      };

      permissions = {
        defaultMode = "auto";
        allow = [
          "Read(~/**)"
          "Read(//tmp/**)"

          "WebSearch"
          "WebFetch(domain:localhost)"
          "WebFetch(domain:private-user-images.githubusercontent.com)"
          "WebFetch(domain:raw.githubusercontent.com)"

          "Bash(bash:*)"
          "Bash(cargo check:*)"
          "Bash(chmod +x:*)"
          "Bash(curl:*)"
          "Bash(file:*)"
          "Bash(find:*)"
          "Bash(git:*)"
          "Bash(gh:*)"
          "Bash(grep:*)"
          "Bash(log show:*)"
          "Bash(ls:*)"
          "Bash(lsof:*)"
          "Bash(open:*)"
          "Bash(pkill:*)"
          "Bash(python3:*)"
          "Bash(sysctl security:*)"
          "Bash(wc:*)"
          "Bash(xargs sh:*)"

          "Bash(d2:*)"
          "WebFetch(domain:d2lang.com)"
          "WebFetch(domain:terrastruct.com)"

          "Bash(go build:*)"
          "Bash(go doc:*)"
          "Bash(go mod:*)"
          "Bash(go test:*)"
          "Bash(go version:*)"
          "Bash(go vet:*)"
          "Bash(gh api:*)"
          "Bash(gh issue:*)"
          "Bash(gh pr:*)"
          "Bash(gh search:*)"

          "Bash(nix:*)"
          "Bash(nix build:*)"
          "Bash(nix eval:*)"
          "Bash(nix flake:*)"
          "Bash(nix-shell:*)"
          "WebFetch(domain:discourse.nixos.org)"
          "WebFetch(domain:docs.rs)"
          "WebFetch(domain:github.com)"
          "WebFetch(domain:mynixos.com)"
          "WebFetch(domain:nixos.org)"
          "WebFetch(domain:wiki.nixos.org)"

          "mcp__claude_ai_Chainguard_Analytics__metrics"
        ];
      };

      env = {
        CLAUDE_CODE_ENABLE_TELEMETRY = "0";
        # Force subagents onto Opus instead of the built-in Haiku default.
        # Read first in the subagent model resolver (io()), so it overrides
        # even the hardcoded-Haiku built-ins (Explore, claude-code-guide).
        # "inherit" would follow the main loop model; an explicit id pins it.
        # Tradeoff: Explore/doc-lookup fan-out gets ~15x pricier + slower.
        CLAUDE_CODE_SUBAGENT_MODEL = "opus";
      };

      spinnerVerbs = {
        mode = "replace";
        verbs = [
          "pillagin'"
          "cookin'"
          "wildin'"
          "burnin'"
          "destroyin'"
          "cultivatin'"
          "confusin'"
          "conflatin'"
          "plottin'"
          "conspirin'"
          "swindlin'"
          "bamboozlin'"
          "hoodwinkin'"
          "finaglin'"
          "ransackin'"
          "hijackin'"
          "racketeerin'"
          "skulkin'"
          "instigatin'"
          "rabble-rousin'"
          "yoinkin'"
          "dissociatin'"
          "spiralin'"
          "catastrophizin'"
          "doomscrollin'"
          "unravelin'"
          "ruminatin'"
          "overthinkin'"
          "jonesin'"
          "hallucinatin'"
          "confabulatin'"
          "regurgitatin'"
          "scrapin'"
          "improvisin'"
          "yappin'"
          "overfittin'"
          "bunglin'"
          "whackin'"
          "blitzin'"
          "slammin'"
          "riffin'"
          "cappin'"
          "schemin'"
          "usurpin'"
          "overthrowin'"
          "commoditizin'"
          "spittin'"
          "trippin'"
        ];
      };
    };
  };

  # Globally gitignore the stray-state homes: compiled binaries the go hooks sweep
  # up, and session scratch docs (TASKS/STATUS/DEVPLAN) written to clodtalk.
  # **/ prefix on the .claude/ entries is load-bearing: .claude/ dirs nest below
  # repo roots (e.g. this repo's homies/.../kitty/.claude/), and a root-anchored
  # pattern (one with a mid-pattern slash) would only match at the repo top.
  # __pycache__/ and *.pyc have no internal separator, so they already match at
  # any depth and need no glob.
  programs.git.ignores = [
    "**/.claude/bin/" # compiled binaries the go hooks sweep up
    "**/.claude/clodtalk/" # session scratch docs (TASKS/STATUS/DEVPLAN)
    "**/.claude/settings.local.json" # per-project local settings (machine/secret-bearing)
    # python detritus clod leaves when it runs or verifies scripts
    "__pycache__/"
    "*.pyc"
  ];

  # Statuslines and the ultra-concise output style, plus every workflow script
  # auto-deployed from ./workflows (see ./workflows/default.nix). No native module
  # option for workflows or output-styles, so home.file.
  home.file = {
    ".claude/statusline.sh" = {
      executable = true;
      source = ./statusline.sh;
    };
    ".claude/subagent-statusline.sh" = {
      executable = true;
      source = ./subagent-statusline.sh;
    };
    ".claude/output-styles/ultra-concise.md".source = ./output-styles/ultra-concise.md;
    ".claude/keybindings.json".source = ./keybindings.json;
  }
  // (import ./workflows { inherit lib; });
}
