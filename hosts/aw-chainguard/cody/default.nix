# Everything cody, one home-manager module: the codex CLI, its nix-owned
# profile layers, CODEX_HOME/AGENTS.md, the agent-role registry, the
# skills it shares with clod, and the clod-side wiring (consult skill +
# Bash allowlist slice). The darwin-side package overlay lives in
# ./overlays.nix -- home-manager modules cannot contribute
# nixpkgs.overlays under useGlobalPkgs.
#
# WHAT NIX OWNS, AND WHAT IT DELIBERATELY DOES NOT
#
# Not owned: ~/.codex/config.toml. codex rewrites it at runtime and its
# writer follows the symlink chain to the final target before creating a
# temp file beside it, so a store symlink there is a permanent EACCES for
# every persisted setting. Left alone, interactive codex works whole.
# Everything nix wants to say lives in the profile layers instead (see
# ./profiles.nix), which only non-interactive callers select.
#
# Owned: the profile files, AGENTS.md, the agent roles, the skills. All
# are leaf entries inside a real, writable ~/.codex, so codex's own state
# (auth.json, the sqlite DBs, sessions/, history.jsonl, models_cache.json,
# skills/.system) sits beside them untouched.
#
# Owned, but outside ~/.codex entirely: the hook estate, which ships as a
# managed config layer in the `com.openai.codex` CFPreferences domain so
# that codex treats it as trust-exempt. See ./hooks.nix.
{
  pkgs,
  lib,
  config,
  ...
}:
let
  profiles = import ./profiles.nix { inherit lib; };

  # The hook binaries are shared with clod -- one glod module, one go-fmt
  # script, one sweep script, serving both harnesses. fancypants and
  # go-fmt-hook handle codex's `apply_patch` payload shape alongside Claude's
  # Write/Edit/MultiEdit; the tool names never collide, so no flavor flag.
  hookBins = import ../clod/hooks { inherit pkgs; };
  codexHooks = import ./hooks.nix {
    inherit lib;
    hooks = hookBins;
  };

  # The hook estate as a TOML fragment, delivered through the macOS
  # managed-preferences layer by the activation step below. `[hooks]` and
  # nothing else -- see ./hooks.nix for why that restraint is load-bearing.
  managedHooksToml = (pkgs.formats.toml { }).generate "cody-managed-hooks.toml" {
    hooks = codexHooks.eventMap;
  };
in
{
  programs.codex = {
    enable = true;
    package = pkgs.codex;

    # hooks is deliberately NOT set: that option writes
    # CODEX_HOME/hooks.json, which is a user-owned source and therefore
    # needs a trust hash. The estate ships through managed preferences
    # instead (see the activation step below). Declaring it in both places
    # would register every handler twice and warn about it.

    # CODEX_HOME/AGENTS.md: the consultant contract (ground every claim,
    # no fabrication, verdict-first, ASCII). codex is almost always
    # invoked BY clod as a cross-model leg, so its instructions mirror the
    # house rigor rules rather than restating repo mechanics -- per-repo
    # AGENTS.md files (and, via project_doc_fallback_filenames, CLAUDE.md)
    # layer on top. Read through a symlink without complaint; unlike the
    # project docs it is loaded with no byte cap.
    context = ./AGENTS.md;

    # settings is left EMPTY on purpose: the module writes
    # ~/.codex/config.toml only when it is non-empty, and that file has to
    # stay codex's. See the header.
    settings = { };

    # $CODEX_HOME/<name>.config.toml, selected with `-p <name>`. There is
    # no default-profile mechanism in 0.153.4 -- no env var, no config
    # key, and the legacy top-level `profile =` selector is fatal -- so
    # every call site names its profile explicitly.
    inherit profiles;

    # Shared verbatim with clod: codex's SKILL.md parser reads the same
    # name/description frontmatter and ignores Claude-only keys, so one
    # source serves both harnesses. `go` is NOT shared -- it documents
    # clod's format/build/vet hook estate, which has no codex counterpart,
    # and a skill describing machinery that is not there is worse than no
    # skill.
    skills = {
      deslop = ../clod/skills/deslop/SKILL.md;
      handoff = ../clod/skills/handoff/SKILL.md;
    };
  };

  # Agent roles, auto-discovered from <config_folder>/agents/**/*.toml --
  # no config.toml entry and no `-p` needed, which is why they are the one
  # part of the harness that applies to interactive codex too. They shape
  # the children the `deep` profile's ultra effort spawns.
  #
  # A SUBdirectory symlink on purpose: ~/.codex/agents stays a real
  # directory, so hand-added roles coexist and codex's own subagent import
  # still has somewhere to write. The .toml files themselves must be real
  # regular files -- the spawn-time read is O_NOFOLLOW and rejects a
  # symlinked role file (core/src/agent/role.rs:144 ->
  # exec-server/src/regular_file.rs:38), and it fails at spawn, not at
  # load, so a symlinked role looks healthy right up until it is used.
  # Linking the directory keeps the files themselves store-resident.
  home.file.".codex/agents/nix".source = ./agents;

  # Hook delivery. codex reads a config layer out of the `com.openai.codex`
  # CFPreferences domain, and treats anything it declares as MANAGED --
  # which is what makes the hooks trust-exempt, with no hash to mint and no
  # per-invocation flag. The domain is the plain per-user one: no MDM, no
  # root, no configuration profile. Verified live.
  #
  # An activation step rather than `targets.darwin.defaults`: that option
  # applies its domain with `defaults import`, which replaces the whole
  # domain, and the base64 would have to be computed at eval time (nix has
  # no base64 builtin, so that means import-from-derivation). Encoding at
  # activation keeps evaluation pure and sets exactly one key, leaving
  # anything else in the domain alone.
  #
  # cfprefsd caches aggressively, so the read-back is the verification that
  # the write actually landed rather than paranoia.
  # The payload also lands as a plain file. codex does not read it there --
  # the CFPreferences copy is what counts -- but it is the exact bytes the
  # activation step encodes, which makes "what hooks does nix think it
  # deployed?" answerable with `cat` instead of `defaults read | base64 -d`,
  # and lets the flake check validate the artifact that actually ships.
  home.file.".codex/nix-managed-hooks.toml".source = managedHooksToml;

  home.activation.codexManagedHooks = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
    blob=$(/usr/bin/base64 < ${managedHooksToml} | /usr/bin/tr -d '\n')
    $DRY_RUN_CMD /usr/bin/defaults write com.openai.codex config_toml_base64 -string "$blob"
    if [ -z "$DRY_RUN_CMD" ] && [ "$(/usr/bin/defaults read com.openai.codex config_toml_base64)" != "$blob" ]; then
      errorEcho "cody: com.openai.codex config_toml_base64 did not take; hooks will not run"
      exit 1
    fi
  '';

  programs.claude-code = {
    skills = {
      # Cross-model second opinion via codex (different vendor, so
      # reviewers do not share the pinned Claude model's blind spots).
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
      # Config/auth triage without a turn: doctor reports the resolved
      # model, the config.toml parse result, and auth state in one pass.
      "Bash(codex doctor:*)"
    ];
  };
}
