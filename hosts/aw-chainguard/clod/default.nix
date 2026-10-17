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
  # Binaries reachable from claude, every Bash tool call, and every subagent,
  # without touching the interactive profile. claude's nixpkgs wrapper already
  # --prefix'es procps+ripgrep onto PATH; the shell snapshot claude sources per
  # Bash call re-exports whatever PATH it inherited, so extending that wrapper
  # reaches agents and nothing else. GNU builds on purpose: agents write GNU
  # flags (`stat -c`, `sed -i` with no arg, `date -d`), and escaping claude's
  # find/grep shims via `command find`/`command grep` otherwise lands on BSD.
  # Duplicating userland packages is deliberate -- this is the agent contract,
  # not a side effect of what happens to be installed.
  agentTools = with pkgs; [
    coreutils
    findutils
    gnugrep
    gnused
    gawk
    ripgrep
    fd
    jq
  ];
  # symlinkJoin, not overrideAttrs: keeps every path the module reads (it gates
  # managed MCP/LSP/plugins on lib.getVersion of this package, so name/version
  # must survive) and re-wraps only bin/claude.
  claudeWithTools = pkgs.symlinkJoin {
    name = "claude-code-${pkgs.claude-code.version}";
    inherit (pkgs.claude-code) version meta;
    paths = [ pkgs.claude-code ];
    nativeBuildInputs = [ pkgs.makeBinaryWrapper ];
    postBuild = ''
      rm $out/bin/claude
      makeWrapper ${pkgs.claude-code}/bin/claude $out/bin/claude \
        --prefix PATH : ${lib.makeBinPath agentTools}
    '';
  };
  # worktrunk's in-repo claude plugin. Upstream ships the manifest at
  # .claude-plugin/plugin.json itself as of f14fa69 (2026-07-22) -- the
  # re-seat this drv used to do -- but cp -RL still materializes any escape
  # symlinks (home-manager's personal-plugin linking would leave them
  # dangling). Same flake input as the wt binary so hooks and CLI never
  # skew.
  worktrunkPlugin = pkgs.runCommand "worktrunk-claude-plugin" { } ''
    cp -RL ${inputs.worktrunk}/plugins/worktrunk $out
    chmod -R u+w $out
  '';
  # Main statusline (settings.statusLine); see ./statusline.nix.
  statusline = import ./statusline.nix { inherit pkgs; };
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
  # plannotator: plan-review plugin + release binary + annotate skill.
  # (cody -- the codex harness -- used to be imported here; it is its own
  # top-level module at ../cody now, imported by the host directly. It
  # still contributes the consult skill and the Bash(codex ...) allowlist
  # slice to programs.claude-code; module contributions merge.)
  imports = [
    ./plannotator
  ];

  programs.claude-code = {
    enable = true;
    package = claudeWithTools;
    context = ./SYSTEM_PROMPT.md;

    # Main-thread voice, in the system prompt rather than the CLAUDE.md user
    # message, and it carries per-turn adherence reminders. Selected by
    # settings.outputStyle below. keep-coding-instructions keeps the built-in
    # engineering block; without it the style REPLACES it. Subagents never load an
    # output style, which is why SYSTEM_PROMPT.md keeps its own VOICE section.
    outputStyles.terse = ./output-styles/terse.md;

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
      # effective-html: self-contained HTML deliverables with a strong visual
      # bias (plans, architecture diagrams, general pages), each skill shipping
      # its reference corpus + agents alongside SKILL.md -- whole-directory
      # deploys, from the pinned effective-html input (never npm/marketplace).
      html = "${inputs.effective-html}/skills/html";
      html-diagram = "${inputs.effective-html}/skills/html-diagram";
      html-plan = "${inputs.effective-html}/skills/html-plan";
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

    # attrset, not list: the key IS the ~/.claude/skills/<name> directory
    # name. The list form derives it from the source basename, which for a
    # bare store path yields the hash-prefixed name -- home-manager warns on
    # it as of ec16115.
    plugins = {
      # sideloaded from the nix input, no marketplace / mutable plugin cache;
      # see worktrunkPlugin above for the escape-symlink materialization
      worktrunk = worktrunkPlugin;
    };

    settings = {
      model = "claude-opus-5-5[1m]";
      modelSettings = {
        "claude-opus-5-5" = {
          effortLevel = "high";
          maxEffortLevel = "max";
          autoCompactWindow = "auto";
        };
        "claude-sonnet-5-5" = {
          effortLevel = "high";
          maxEffortLevel = "max";
          autoCompactWindow = "auto";
        };
        "claude-haiku-5-5" = {
          effortLevel = "medium";
          maxEffortLevel = "max";
          autoCompactWindow = "auto";
        };
        "claude-fable-5-1" = {
          effortLevel = "high";
          maxEffortLevel = "max";
          autoCompactWindow = "auto";
        };
      };
      outputStyle = "terse";
      tui = "fullscreen";
      autoScrollEnabled = true;
      includeCoAuthoredBy = false;
      spinnerTipsEnabled = false;
      # ctrl+g external-editor buffer: prepend the last assistant response as
      # #-commented context above the prompt (stripped on save). Key name from the
      # binary's /config panel id ("Show last response in external editor") -- not
      # yet in the schemastore schema, which lags the app.
      externalEditorContext = true;
      # WebFetch sends the requested hostname to api.anthropic.com for a safety
      # preflight on every fetch. Skipped as a latency choice -- and honestly:
      # on this host nothing else scopes WebFetch domains (MDM ignores the
      # allow block below and the managed policy carries no WebFetch rules),
      # so this drops the only remaining domain check, not a redundant one.
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
        # timeout: the harness default is 60s and a killed hook cannot emit
        # decision=block (silent fail-open on exactly the slow runs); gocheck's
        # internal budget is up to 120s per module root, so give it real room.
        Stop = [
          {
            hooks = [
              {
                type = "command";
                command = "${hooks.glod}/bin/gocheck";
                timeout = 300;
              }
            ];
          }
        ];
        # Same gate on subagent ends: Stop and SubagentStop are split events,
        # so without this a workflow executor's Go edits finish ungated and
        # breakage surfaces only at the main-loop stop after synthesis. gocheck
        # echoes the incoming event and never drains the session-wide queue on
        # SubagentStop (clean pass or re-fire) -- only the main Stop drains, so
        # concurrent editors cannot lose queued files to a subagent's pass.
        SubagentStop = [
          {
            hooks = [
              {
                type = "command";
                command = "${hooks.glod}/bin/gocheck";
                timeout = 300;
              }
            ];
          }
        ];
      };

      # HONORED ONLY WHERE NO MDM POLICY REIGNS. This host carries
      # Chainguard managed settings (/Library/Application Support/
      # ClaudeCode/managed-settings.json) with
      # allowManagedPermissionRulesOnly: user/project/local allow rules --
      # this entire block -- are IGNORED there; the managed defaultMode
      # "auto" classifier is what keeps daily work promptless, and it
      # re-prompts on every mutation-shaped command (git commit, push)
      # with no memory across calls. Diagnosed 2026-07-10: recurring
      # `git add ... && git commit` prompts are org policy, not a missing
      # allow entry -- the fix lives in the org's server-managed rules.
      # The block stays: it is the correct config wherever policy lifts.
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

          "Bash(nix:*)"
          "Bash(nix-shell:*)"
          "Bash(darwin-rebuild:*)"
          "Bash(home-manager:*)"
          "WebFetch(domain:discourse.nixos.org)"
          "WebFetch(domain:docs.rs)"
          "WebFetch(domain:github.com)"
          "WebFetch(domain:mynixos.com)"
          "WebFetch(domain:nixos.org)"
          "WebFetch(domain:wiki.nixos.org)"
        ];
      };

      env = {
        # Bash-tool executor: pinned bash, not detected zsh. zsh's `=` expansion
        # kills `echo ===foo` and NOMATCH aborts on an unmatched glob; detection
        # would otherwise land on /bin/zsh or Apple's bash 3.2. Undocumented, and
        # claude falls back to detection if the path lacks bash/zsh -- recheck on
        # bumps. Interactive shell stays zsh.
        CLAUDE_CODE_SHELL = "${pkgs.bashInteractive}/bin/bash";

        # Local OpenTelemetry master switch.
        CLAUDE_CODE_ENABLE_TELEMETRY = "1";

        # Anthropic-bound non-inference telemetry: all off.
        CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC = "1";
        DISABLE_TELEMETRY = "1";
        DISABLE_ERROR_REPORTING = "1";
        CLAUDE_CODE_DISABLE_FEEDBACK_SURVEY = "1";
        DO_NOT_TRACK = "1";
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
  ];

  # The harness is named clod; `claude` is just the binary it wraps.
  programs.zsh.shellAliases.clod = "claude";

  # Statuslines, keybindings, the generated workflow cheatsheet, and every workflow
  # script auto-deployed from ./workflows (see ./workflows/default.nix). No native
  # module option for workflows, so home.file.
  home.file = {
    ".claude/statusline.sh".source = "${statusline}/bin/clod-statusline";
    ".claude/subagent-statusline".source = "${subagentStatusline}/bin/subagent-statusline";
    ".claude/keybindings.json".source = ./keybindings.json;
    # cheatsheet.json: generated from every workflow's meta.flags at build time;
    # consumed by `clod-cheat` and the kitty overlay key.
    ".claude/workflows/cheatsheet.json".source = import ./workflows/cheatsheet.nix { inherit pkgs; };
  }
  // (import ./workflows { inherit lib; });
}
