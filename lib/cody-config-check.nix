# Deployed-artifact validation for the cody (codex) harness, wired by
# mkchecks.nix into a `cody-config-<host>` flake check. Runs against the
# RENDERED home files and the PINNED codex source, so it tests what
# actually deploys against the schema the actual binary enforces.
#
# Six legs, each pinning a failure mode that is silent at runtime:
#
# 1. No ~/.codex/config.toml. Nix must not own that file: codex rewrites
#    it at runtime and its writer dereferences the symlink chain before
#    creating a temp file beside the target, so a store symlink there is a
#    permanent EACCES for /model, folder trust, notice dismissals,
#    `codex mcp add` and `codex features enable`. Reintroducing it would
#    break nothing at build time and everything interactively.
# 2. Profile keys validate against the pinned CLI's own config.schema.json
#    (additionalProperties:false). This is the hermetic stand-in for
#    `codex exec --strict-config`, which needs auth and the network:
#    unknown config keys are SILENTLY DROPPED at runtime otherwise.
# 3. Role files are real regular files. The spawn-time read is O_NOFOLLOW
#    and rejects a symlinked role -- and it fails at SPAWN, not at load,
#    so a symlinked role looks healthy until the moment it is used.
# 4. Role files carry the fields codex requires of a discovered role
#    (name, description, developer_instructions) and register under the
#    name their filename implies -- the file's `name` wins over the
#    directory entry, so a mismatch yields a role nobody can address.
# 5. Every model slug resolves in the pinned catalog and supports the
#    effort asked of it. A bad effort is not a load error: the enum has an
#    open fallback, so a typo becomes Custom("...") and ships to the API.
# 6. Each skill is a DIRECTORY holding a real SKILL.md. codex's discovery
#    walk skips symlinked non-directories with no warning, so a skill
#    deployed as a symlinked file is simply invisible.
# 7. The managed hook payload is a well-formed `[hooks]`-only TOML whose
#    every command exists and is executable, and no hooks.json shadows it.
#    Two failure modes, both silent: a payload carrying any other top-level
#    key would become unoverridable config (that layer outranks the user's
#    own config.toml AND `-c`), and declaring the same hooks in hooks.json
#    as well would register every handler twice.
{
  pkgs,
  homeFiles,
  codexPkg,
}:
let
  python = pkgs.python3.withPackages (ps: [ ps.jsonschema ]);
in
pkgs.runCommand "cody-config-check"
  {
    nativeBuildInputs = [ python ];
  }
  ''
    export CODEX_DIR=${homeFiles}/.codex
    export SCHEMA=${codexPkg.src}/codex-rs/core/config.schema.json
    export MODELS=${codexPkg.src}/codex-rs/models-manager/models.json
    python3 ${./cody-config-check.py}
    touch $out
  ''
