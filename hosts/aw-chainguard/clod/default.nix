{
  lib,
  pkgs,
  inputs,
  config,
  ...
}:
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
  # clod-cheat: a --help-style dump of the workflow cheatsheet, rendered from
  # ~/.claude/workflows/cheatsheet.json (generated from each workflow's meta.flags
  # by ./workflows/cheatsheet.nix). Optional arg filters by workflow name. Bound to
  # a kitty overlay key (see ../../../homies/shimmerjs/home/kitty/default.nix).
  clodCheat = pkgs.writeShellApplication {
    name = "clod-cheat";
    runtimeInputs = [ pkgs.jq ];
    text = ''
      sheet="$HOME/.claude/workflows/cheatsheet.json"
      [ -f "$sheet" ] || { echo "no cheatsheet at $sheet (rebuild clod)" >&2; exit 1; }
      jq -r --arg f "''${1:-}" '
        .workflows[]
        | select($f == "" or (.name | ascii_downcase | contains($f | ascii_downcase)))
        | "\(.name)  --  \(.description)",
          "  when: \(.whenToUse)",
          (.flags[] | "    \(.short)|\(.name)  \(.type)  default=[\(.default)]\(if .range != "" then "  \(.range)" else "" end)  \(.help)"),
          (.examples[] | "    e.g. \(.)"),
          ""
      ' "$sheet"
    '';
  };
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

    skills = {
      # Cross-session/compaction continuity. Model-authored handoff with a fixed
      # schema (VERIFIED vs ASSUMED, next action), written to the work-docs home
      # (<worktree_root>/docs/handoffs/, OS temp dir fallback), referencing
      # artifacts by path.
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
      # Memory hygiene: audit the persistent memory index + per-fact files and
      # prune one-off / superseded / repo-duplicated entries. Guardrailed
      # (keep-when-unsure); answers the recurring manual "prune your memory" ask.
      memory-prune = ./skills/memory-prune/SKILL.md;
      # Cross-model second opinion via codex (different vendor, so reviewers don't
      # share the pinned Claude model's blind spots). CLI-only via the
      # Bash(codex ...) perms; threading via `codex exec resume <uuid>`. No MCP
      # server: per-session stdio daemons cost ~32MB each for a rarely-used path,
      # and the hm module's plugin-prefixed tool names broke the allowlist silently.
      codex-consult = ./skills/codex-consult/SKILL.md;
    };

    mcpServers = {
      linear = {
        type = "http";
        url = "https://mcp.linear.app/mcp";
      };
    };

    # Byte-for-byte what gopls-lsp@claude-plugins-official provides: that plugin is
    # an empty dir whose whole definition is this lspServers block in the
    # marketplace.json. Declaring it here drops the marketplace / mutable plugin
    # cache dependency; the gopls binary comes from go-tools.nix.
    lspServers = {
      gopls = {
        command = "gopls";
        extensionToLanguage.".go" = "go";
      };
    };

    plugins = [
      # worktrunk's in-repo claude plugin, pinned to the same flake input as the
      # wt binary so hooks and CLI never skew. Skips marketplace / mutable plugin
      # cache by sideloading via --plugin-dir directly from nix inputs.
      "${inputs.worktrunk}/plugins/worktrunk"
    ];

    settings = {
      apiKeyHelper = "/usr/bin/security find-generic-password -s anthropic-api-key -w";
      model = "claude-fable-5";
      effortLevel = "xhigh";
      autoScrollEnabled = true;
      includeCoAuthoredBy = false;
      # ctrl+g external-editor buffer: prepend the last assistant response as
      # #-commented context above the prompt (stripped on save). Key name from the
      # binary's /config panel id ("Show last response in external editor") -- not
      # yet in the schemastore schema, which lags the app.
      externalEditorContext = true;
      # WebFetch sends the requested hostname to api.anthropic.com for a safety
      # preflight on every fetch. The permission allowlist already scopes which
      # domains are reachable, so the preflight is largely redundant.
      skipWebFetchPreflight = true;
      statusLine = {
        type = "command";
        command = "~/.claude/statusline.sh";
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
        # fancypants: deny decorative Unicode and banner/divider comments in
        # anything clod writes, every project. Bash is matched too so it also
        # covers git-commit messages and gh pr/issue/release bodies -- prose
        # channels that bypass the file-write matcher (the hook self-filters to
        # those subcommands).
        PreToolUse = [
          {
            matcher = "Write|Edit|MultiEdit|Bash";
            hooks = [
              {
                type = "command";
                command = "${hooks.glod}/bin/fancypants";
              }
            ];
          }
          # Force scriptPath over name= for deployed workflows: name resolution
          # is frozen at session start and can run a stale pre-switch script.
          {
            matcher = "Workflow";
            hooks = [
              {
                type = "command";
                command = "${hooks.awScriptpathGate}/bin/clod-aw-scriptpath-gate";
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

          "Bash(cue:*)"
          "Bash(jq:*)"

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
          "Bash(darwin-rebuild:*)"
          "Bash(home-manager:*)"
          "WebFetch(domain:discourse.nixos.org)"
          "WebFetch(domain:docs.rs)"
          "WebFetch(domain:github.com)"
          "WebFetch(domain:mynixos.com)"
          "WebFetch(domain:nixos.org)"
          "WebFetch(domain:wiki.nixos.org)"

          "mcp__claude_ai_Chainguard_Analytics__metrics"

          # codex-consult: CLI-only, promptless (exec resume carries threads).
          "Bash(codex exec:*)"
          "Bash(codex review:*)"
          "Bash(codex login status:*)"
        ];
      };

      env = {
        # Local OpenTelemetry master switch.
        CLAUDE_CODE_ENABLE_TELEMETRY = "1";

        # Anthropic-bound non-inference telemetry: all off.
        CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC = "1";
        DISABLE_TELEMETRY = "1";
        DISABLE_ERROR_REPORTING = "1";
        CLAUDE_CODE_DISABLE_FEEDBACK_SURVEY = "1";
        DO_NOT_TRACK = "1";

        # "inherit": the custom agents (no model: frontmatter) follow the
        # main-loop model.
        # Tradeoff: the hardcoded-Haiku built-ins (Explore, claude-code-guide)
        # drop back to Haiku. Change this to a specific model to force all
        # agents to use a specific model unconditionally.
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

  # Globally gitignore the stray-state homes: compiled binaries the go hooks
  # sweep up. (Work/session docs live outside repos entirely --
  # <worktree_root>/docs/ or the OS temp dir -- so they need no ignore.)
  # **/ prefix on the .claude/ entries is load-bearing: .claude/ dirs nest below
  # repo roots (e.g. this repo's homies/.../kitty/.claude/), and a root-anchored
  # pattern (one with a mid-pattern slash) would only match at the repo top.
  # __pycache__/ and *.pyc have no internal separator, so they already match at
  # any depth and need no glob.
  programs.git.ignores = [
    "**/.claude/bin/" # compiled binaries the go hooks sweep up
    "**/.claude/settings.local.json" # per-project local settings (machine/secret-bearing)
    # python detritus clod leaves when it runs or verifies scripts
    "__pycache__/"
    "*.pyc"
  ];

  home.packages = [
    clodCheat
    # codex CLI on PATH for codex-consult (exec / review / exec resume; no MCP).
    pkgs.codex
  ];

  # Statuslines, keybindings, the generated workflow cheatsheet, and every workflow
  # script auto-deployed from ./workflows (see ./workflows/default.nix). No native
  # module option for workflows, so home.file.
  home.file = {
    ".claude/statusline.sh" = {
      executable = true;
      source = ./statusline.sh;
    };
    ".claude/subagent-statusline".source = "${subagentStatusline}/bin/subagent-statusline";
    ".claude/keybindings.json".source = ./keybindings.json;
    # cheatsheet.json: generated from every workflow's meta.flags at build time;
    # consumed by `clod-cheat` and the kitty overlay key.
    ".claude/workflows/cheatsheet.json".source = import ./workflows/cheatsheet.nix { inherit pkgs; };
  }
  // (import ./workflows { inherit lib; });
}
