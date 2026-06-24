# Builds the clod workflow cheatsheet JSON (via cheatsheet.nix -> cheatsheet-gen.mjs)
# and asserts it has one entry per aw-*.js, each with a name and a non-empty flags
# list. Catches meta-extraction drift (a reflowed meta literal, a missing flags
# block) at `nix flake check` time. Produced by mkchecks.nix as clod-cheatsheet-<host>.
{ pkgs, cheatsheet, expected }:
pkgs.runCommand "clod-cheatsheet-check"
  { nativeBuildInputs = [ pkgs.jq ]; inherit cheatsheet expected; }
  ''
    n=$(jq '.workflows | length' "$cheatsheet")
    if [ "$n" != "$expected" ]; then
      echo "clod cheatsheet check FAILED: $n workflow(s) in cheatsheet.json, expected $expected aw-*.js" >&2
      exit 1
    fi
    bad=$(jq -r '.workflows[] | select((.flags | length) == 0 or .name == "") | (.name // "<unnamed>")' "$cheatsheet")
    if [ -n "$bad" ]; then
      echo "clod cheatsheet check FAILED: workflow(s) missing flags/name: $bad" >&2
      exit 1
    fi
    echo "clod cheatsheet OK ($n workflows, all with flags):"
    jq -r '.workflows[] | "  - \(.name): \(.flags | length) flags, \(.examples | length) examples"' "$cheatsheet"
    touch $out
  ''
