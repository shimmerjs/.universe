# Harness-variant experiment system. A variant is the shared clod config with a few
# settings overridden, run under its OWN CLAUDE_CONFIG_DIR so its sessions land in a
# separate projects/ transcript tree -- self-describing per variant, so cross-variant
# stats (turns, durations, tokens for "like" prompts) need no hand-labeling. See the
# clod-variant-stats launcher.
#
# Why config-dir per variant (not a --settings overlay): in current Claude Code a
# process-env export does NOT beat the settings.json env block (issues #8500,
# lmstudio #561), and --settings env-merge semantics are unverified (#19487). A full
# per-variant settings.json is deterministic under any merge model, and
# CLAUDE_CONFIG_DIR is the only lever that ALSO segregates transcripts. Costs: the
# on-demand background daemon is disabled when CLAUDE_CONFIG_DIR is set, marketplace
# plugins (e.g. gopls-lsp) are not shared into a variant dir, and plain `claude`
# (base ~/.claude) is intentionally left untouched. apiKeyHelper auth works across
# dirs, so no re-login.
#
# A variant = { description; settings; } where settings is a SHALLOW override merged
# over base settings (top-level keys replace wholesale). For env, provide the
# COMPLETE block -- mkEnv/rmEnv build it from base. Returns { launchers, configDirs }
# which clod/default.nix merges into home.packages / home.file.
{ lib, pkgs, config }:
let
  base = config.programs.claude-code.settings;
  schema = "https://json.schemastore.org/claude-code-settings.json";
  jsonFmt = pkgs.formats.json { };

  mkEnv = overrides: (base.env or { }) // overrides;
  rmEnv = keys: removeAttrs (base.env or { }) keys;

  # The variant matrix. First axis: subagent-model routing (the inherit-vs-delegate
  # A/B). Generalizes to any settings/env combo -- add a variant with its override.
  variants = {
    subagent-inherit = {
      description = "base behavior: custom agents follow the main model (Opus); per-stage model overrides work";
      settings = { };
    };
    subagent-auto = {
      description = "CLAUDE_CODE_SUBAGENT_MODEL unset, so built-ins (Explore, claude-code-guide) auto-delegate to Haiku";
      settings = { env = rmEnv [ "CLAUDE_CODE_SUBAGENT_MODEL" ]; };
    };
  };

  variantSettings = v: base // v.settings // { "$schema" = schema; };

  sym = p: config.lib.file.mkOutOfStoreSymlink "${config.home.homeDirectory}/.claude/${p}";

  # A real config dir that regenerates only settings.json and symlinks the rest of
  # the static config back to base ~/.claude (statusline + hooks are referenced by
  # absolute ~/.claude path inside settings.json, so they need no symlink). projects/
  # is deliberately NOT linked: claude creates it per variant, which is the point.
  configDirOf = name: v: {
    ".claude-${name}/settings.json".source = jsonFmt.generate "clod-${name}-settings.json" (variantSettings v);
    ".claude-${name}/CLAUDE.md".source = sym "CLAUDE.md";
    ".claude-${name}/agents".source = sym "agents";
    ".claude-${name}/skills".source = sym "skills";
    ".claude-${name}/workflows".source = sym "workflows";
    ".claude-${name}/keybindings.json".source = sym "keybindings.json";
  };

  # clod-<name>: run claude (the wrapped finalPackage on PATH, with its --plugin-dir
  # args intact) against the variant's config dir.
  launcherOf = name: _: pkgs.writeShellScriptBin "clod-${name}" ''
    exec env CLAUDE_CONFIG_DIR="$HOME/.claude-${name}" claude "$@"
  '';

  # Aggregate per-variant turns/duration/tokens from each variant's segregated
  # transcripts (~/.claude-<name>/projects/**/*.jsonl). turn_duration system records
  # carry durationMs + messageCount; assistant records carry message.usage tokens.
  statsHelper = pkgs.writeShellApplication {
    name = "clod-variant-stats";
    runtimeInputs = [ pkgs.jq pkgs.findutils pkgs.coreutils ];
    text = ''
      shopt -s nullglob
      printf '%-22s %9s %8s %12s %13s\n' variant sessions turns duration_s out_tokens
      for d in "$HOME"/.claude-*/; do
        [ -d "''${d}projects" ] || continue
        variant=$(basename "$d" | sed 's/^\.claude-//')
        mapfile -t files < <(find "''${d}projects" -name '*.jsonl' -type f 2>/dev/null)
        if [ ''${#files[@]} -eq 0 ]; then
          printf '%-22s %9s %8s %12s %13s\n' "$variant" 0 0 0 0
          continue
        fi
        cat "''${files[@]}" 2>/dev/null | jq -rs '
          { sessions: ([.[].sessionId] | map(select(. != null)) | unique | length),
            turns:    ([.[] | select(.subtype == "turn_duration")] | length),
            dur:      (([.[] | select(.subtype == "turn_duration") | .durationMs] | add) // 0),
            out:      (([.[] | select(.type == "assistant") | .message.usage.output_tokens] | add) // 0) }
          | "\(.sessions)\t\(.turns)\t\((.dur / 1000 | floor))\t\(.out)"
        ' | while IFS="$(printf '\t')" read -r s t dur out; do
          printf '%-22s %9s %8s %12s %13s\n' "$variant" "$s" "$t" "$dur" "$out"
        done
      done
    '';
  };
in
{
  launchers = lib.mapAttrsToList launcherOf variants ++ [ statsHelper ];
  configDirs = lib.foldl' (a: b: a // b) { } (lib.mapAttrsToList configDirOf variants);
}
