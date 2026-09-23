# The cody hook estate.
#
# Hooks are delivered through the macOS managed-preferences config layer
# (`com.openai.codex` / `config_toml_base64`, read via CFPreferences at
# config/src/loader/macos.rs:20-22), NOT through $CODEX_HOME/hooks.json.
# That one choice removes the whole trust problem: codex refuses to run a
# hook from a user-owned source unless `hooks.state."<key>".trusted_hash`
# matches a hash it recomputes, but a hook from a MANAGED layer is
# trust-exempt -- `is_managed` short-circuits the hash check entirely
# (hooks/src/engine/discovery.rs:802-803, 825-838). So there is no hash to
# mint, nothing to keep in sync with the hook binaries, and no `-c` flag.
#
# Verified live: a `[hooks]` table in that preference, with no hooks.json
# and no hooks.state anywhere, runs the hook. The preference is readable
# from the plain per-user domain -- no MDM, no root, no configuration
# profile.
#
# Why not `-c 'hooks.state={...}'`, which also works: any `-c` override
# forces the TUI back to an in-process app-server
# (tui/src/lib.rs:919-930), and if a session ever attaches to a shared one
# via `--remote` or `codex agents`, client-side `-c` is filtered to a
# typed ConfigOverrides struct on the way across the wire -- `hooks` is not
# in it, so trust would silently stop arriving. Hook discovery, trust, and
# execution all resolve from the SERVING process's config stack. A
# per-user CFPreferences layer is read by whichever process serves the
# session, so managed delivery survives that topology change.
#
# KEEP THE PAYLOAD TO `[hooks]` ONLY. This layer is
# LegacyManagedConfigTomlFromMdm at precedence 50
# (config/src/config_layer_source.rs:33-50), above the user's own
# config.toml (20) and above `-c` (30). Anything put here becomes
# unoverridable, which is right for a guardrail and wrong for everything
# else. The flake check fails on any other top-level key.
{
  lib,
  hooks,
}:
let
  # One handler per group. Timeouts are explicit everywhere: codex defaults
  # to 600s for every event except SessionEnd/Interrupt, and a synchronous
  # hook at that default can stall a turn for ten minutes.
  specs = [
    # The ASCII / no-banner-comment gate over everything cody writes.
    # `apply_patch` is codex's file-write tool; `Bash` catches the
    # git-commit and gh pr/issue prose channels, as on the clod side.
    # Matcher alternation is exact-match, not regex -- the string is word
    # characters and pipes only.
    {
      event = "PreToolUse";
      matcher = "Bash|apply_patch";
      command = "${hooks.glod}/bin/fancypants";
      timeout = 10;
    }
    # gofmt syntax gate + goimports, and queue the file for the Stop pass.
    {
      event = "PostToolUse";
      matcher = "apply_patch";
      command = "${hooks.goFmtHook}/bin/clod-go-fmt-hook";
      timeout = 60;
    }
    # Sweep stray `go build` binaries out of the tree.
    {
      event = "PostToolUse";
      matcher = "Bash";
      command = "${hooks.goBuildSweep}/bin/clod-go-build-sweep";
      timeout = 60;
    }
    # build+vet the packages touched this turn; blocks the turn on a
    # diagnostic. gocheck's own budget runs to 120s per module root.
    {
      event = "Stop";
      command = "${hooks.glod}/bin/gocheck";
      timeout = 300;
    }
    {
      event = "SubagentStop";
      command = "${hooks.glod}/bin/gocheck";
      timeout = 300;
    }
  ];

  events = lib.unique (map (s: s.event) specs);
  specsFor = event: lib.filter (s: s.event == event) specs;

  groupOf = s: {
    hooks = [
      {
        type = "command";
        inherit (s) command;
        timeout = s.timeout;
        async = false;
      }
    ];
  }
  // lib.optionalAttrs (s ? matcher) { inherit (s) matcher; };
in
{
  # The `[hooks]` table: event -> ordered matcher groups -> command
  # handlers. Same outer shape as Claude's settings.json hooks block.
  eventMap = lib.listToAttrs (map (event: lib.nameValuePair event (map groupOf (specsFor event))) events);
}
