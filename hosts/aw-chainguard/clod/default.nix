{ lib, pkgs, ... }:
let
  # writeShellApplication-wrapped hook programs (nix-pinned binaries), defined in
  # ./hooks/default.nix alongside the scripts they wrap.
  hooks = import ./hooks { inherit pkgs; };
  # Agent-panel row renderer (settings.subagentStatusLine). Go: one process per
  # refresh tick regardless of task count; unit tests run in the check phase.
  subagentStatusline = pkgs.buildGoModule {
    pname = "subagent-statusline";
    version = "0.1.0";
    src = ./subagent-statusline;
    vendorHash = null;
  };
in
{
  programs.claude-code = {
    enable = true;
    # Temporary pin: Fable 5 (claude-fable-5) needs claude-code >= 2.1.170, but nixpkgs
    # is still on 2.1.161 (bump tracked in nixpkgs#530023). Override with the upstream
    # prebuilt binary until that lands, then delete this block and the package follows
    # nixpkgs again. darwin-arm64 only -- this host is aarch64-darwin.
    package =
      let
        v = "2.1.170";
      in
      pkgs.claude-code.overrideAttrs (_: {
        version = v;
        src = pkgs.fetchurl {
          url = "https://downloads.claude.ai/claude-code-releases/${v}/darwin-arm64/claude";
          hash = "sha256-6QNkbYt6MYgqgOzSdWmifYrFezcIdF80lwljLIQRf98=";
        };
      });
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
      # referencing artifacts by path.
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
      model = "fable";
      effortLevel = "xhigh";
      autoScrollEnabled = true;
      statusLine = {
        type = "command";
        command = "~/.claude/statusline.sh";
        # Renders instantly from cache. The timer only matters while the session
        # is idle (background workflow running, no events firing): it keeps the
        # spend ledger and live agent/workflow row ticking. 30s balances that
        # against render cost.
        refreshInterval = 30;
      };
      # Rich per-agent rows in the subagent panel during fan-out / workflows.
      subagentStatusLine = {
        type = "command";
        command = "~/.claude/subagent-statusline";
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
        # "inherit" disables the force, so normal resolution applies: the custom agents
        # (no model: frontmatter) follow the main loop (Fable 5) and per-spawn / per-
        # workflow model overrides work again. Tradeoff: the hardcoded-Haiku built-ins
        # (Explore, claude-code-guide) drop back to Haiku -- the thing the old "opus"
        # pin existed to override. Set "fable"/"best" to put those on Fable too, or
        # "opus" to cap all fan-out at the cheaper Opus 4.8 ($5/$25 vs $10/$50).
        CLAUDE_CODE_SUBAGENT_MODEL = "inherit";
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
    ".claude/subagent-statusline".source = "${subagentStatusline}/bin/subagent-statusline";
    ".claude/output-styles/ultra-concise.md".source = ./output-styles/ultra-concise.md;
    ".claude/keybindings.json".source = ./keybindings.json;
  }
  // (import ./workflows { inherit lib; });
}
