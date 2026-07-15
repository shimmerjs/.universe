# Drift guard for pkgs/krib/sheets: every committed <name>.json (the
# go:embed'd loadable artifact) must equal `cue export` of its sibling
# <name>.cue source. Regenerate with `cue export <name>.cue -o <name>.json`.
{ pkgs }:

pkgs.runCommand "krib-sheets-check"
  {
    nativeBuildInputs = [ pkgs.cue ];
    sheets = ../pkgs/krib/sheets;
  }
  ''
    status=0
    for cuefile in "$sheets"/*.cue; do
      name=$(basename "$cuefile" .cue)
      cue export "$cuefile" -o "exported-$name.json"
      if ! diff -u "$sheets/$name.json" "exported-$name.json"; then
        echo "krib sheet $name.json drifted from $name.cue (regenerate with cue export)" >&2
        status=1
      fi
    done
    [ "$status" -eq 0 ] && touch $out
  ''
