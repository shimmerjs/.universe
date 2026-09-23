# Profile-v2 files: $CODEX_HOME/<name>.config.toml, each a complete config
# layer selected with `codex exec -p <name>`. This is the entire nix-owned
# settings surface.
#
# ~/.codex/config.toml is deliberately NOT managed. codex rewrites it from
# ~20 call sites and its writer dereferences the symlink chain before
# writing (core/src/config/edit.rs:749 -> utils/path-utils/src/lib.rs:47),
# so a store symlink there makes every persisted setting fail with EACCES
# against /nix/store: /model, theme, keymap, folder trust, notice
# dismissals, `codex mcp add`, `codex features enable`. Leaving it alone
# keeps interactive codex whole.
#
# A profile file is not write-free either -- under `-p` it becomes the
# active user layer and the TUI's persistence paths retarget onto it. That
# costs nothing here because only NON-INTERACTIVE callers pass `-p`, and
# `codex exec` never persists config.
#
# Profiles layer over the base user config, not over each other, so each
# one carries the full baseline. Precedence (config_layer_source.rs:33-51):
# packaged -10 < mdm 0 < system 10 < user 20 < user+profile 21 < project 25
# < -c flags 30. An in-repo .codex/config.toml therefore outranks these.
{ lib }:
let
  # gpt-6-astra is the catalog's "most capable model for complex, demanding
  # work"; `max` is the highest effort that does NOT auto-delegate. `ultra`
  # is max plus automatic task delegation, which only earns its spend in a
  # standalone consult -- inside a leg clod already fanned out it buys
  # tokens, not independence, so it lives in `deep` alone.
  base = {
    model = "gpt-6-astra";
    model_reasoning_effort = "max";

    # Pinned rather than inherited: the `personality` feature is on by
    # default and resolves to pragmatic, but pinning keeps the voice in
    # config instead of in the binary's default table.
    personality = "pragmatic";

    # cody reads a repo's CLAUDE.md when it has no AGENTS.md, so a consult
    # inherits the same repo contract clod works under.
    project_doc_fallback_filenames = [ "CLAUDE.md" ];

    # nix owns the binary; the startup update nag can only be noise.
    check_for_update_on_startup = false;

    # Telemetry opt-outs, the codex-side counterpart to clod's settings.env
    # block. Both are TABLES despite reading like booleans.
    analytics.enabled = false;
    feedback.enabled = false;

    # codex unpacks 6 embedded sample skills into $CODEX_HOME/skills/.system
    # on every startup (wiping the directory first) and their descriptions
    # eat the skill catalog's token budget, which defaults to 2% of the
    # context window. Off: the budget belongs to our own skills.
    skills.bundled.enabled = false;

    # The web-search MODE is this top-level key. `tools.web_search = true`
    # -- which this config used to set -- is dead: the deserializer matches
    # the bool variant and discards it (config_toml.rs:644-660), and
    # resolve_web_search_mode never consults tools.web_search
    # (core/src/config/mod.rs:2618). Default is "cached".
    web_search = "cached";
  };
in
{
  # Cross-model finder / dissent legs: aw-review, aw-audit,
  # aw-design-review, and aw-implement's review phase. No live search --
  # a code review should read the code, not wander.
  review = base;

  # Search legs: aw-research, aw-prior-art. The only difference that
  # matters is live search instead of the cached index.
  research = base // {
    web_search = "live";
  };

  # Standalone hard consults invoked by hand. ultra auto-delegates, which
  # is the point: the agent roles in ./agents are what those spawned
  # children run under.
  deep = base // {
    model_reasoning_effort = "ultra";
    web_search = "live";
    agents = {
      enabled = true;
      # Bounded on purpose -- ultra will use every slot it is given.
      max_concurrent_threads_per_session = 4;
    };
  };

  # Cheap and fast: a yes/no sanity check where a max-effort consult is
  # wasted. sol is the account's priority-0 model.
  triage = base // {
    model = "gpt-5.6-sol";
    model_reasoning_effort = "low";
  };
}
