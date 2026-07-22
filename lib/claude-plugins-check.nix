# Deployed-artifact integrity for the claude-code plugin estate: every
# entry under the rendered ~/.claude/skills must load under claude-code's
# real discovery rules -- manifests seated at .claude-plugin/plugin.json,
# hook/skill pointers resolving, no dangling symlinks, no hash-prefixed
# store basenames leaking in as entry names. Pins the regression class the
# 2026-07 home-manager personal-plugin rewire shipped silently (a plugin's
# relative escape symlink dangling after the join; manifests at the
# ignored plugin root killing hooks) plus the worktrunk skills/hooks
# specifically, since those broke without any eval warning.
{ pkgs, homeFiles }:
pkgs.runCommand "claude-plugins-check" { nativeBuildInputs = [ pkgs.jq ]; } ''
  skills=${homeFiles}/.claude/skills
  [ -d "$skills" ] || { echo "FAIL: no .claude/skills in rendered home files"; exit 1; }
  fail=0
  for entry in "$skills"/*; do
    name=$(basename "$entry")
    if [[ "$name" =~ ^[a-z0-9]{32}- ]]; then
      echo "FAIL: hash-prefixed entry $name (plugin store basename leaked)"; fail=1
    fi
    if [ -d "$entry/.claude-plugin" ]; then
      m="$entry/.claude-plugin/plugin.json"
      if [ -e "$m" ]; then
        jq -e . "$m" > /dev/null || { echo "FAIL: $name manifest is not valid json"; fail=1; }
        hooksPtr=$(jq -r '.hooks // empty' "$m")
        if [ -n "$hooksPtr" ] && [ ! -e "$entry/$hooksPtr" ]; then
          echo "FAIL: $name hooks pointer $hooksPtr does not resolve"; fail=1
        fi
        for s in $(jq -r '.skills[]? // empty' "$m"); do
          [ -f "$entry/$s/SKILL.md" ] || { echo "FAIL: $name skill pointer $s has no SKILL.md"; fail=1; }
        done
      fi
      dangling=$(find -L "$entry" -type l || true)
      if [ -n "$dangling" ]; then
        echo "FAIL: dangling symlinks under plugin $name:"; echo "$dangling"; fail=1
      fi
    else
      [ -f "$entry/SKILL.md" ] || { echo "FAIL: skill $name has no SKILL.md"; fail=1; }
    fi
  done
  # load-bearing pins: the three plugin entries, and worktrunk's full set
  for want in worktrunk hook claude-code-home-manager; do
    [ -e "$skills/$want" ] || { echo "FAIL: expected plugin entry $want missing"; fail=1; }
  done
  [ -f "$skills/worktrunk/skills/worktrunk/SKILL.md" ] || { echo "FAIL: worktrunk skill gone"; fail=1; }
  [ -f "$skills/worktrunk/skills/wt-switch-create/SKILL.md" ] || { echo "FAIL: wt-switch-create skill gone"; fail=1; }
  [ -e "$skills/worktrunk/hooks/claude-hooks.json" ] || { echo "FAIL: worktrunk hooks file gone"; fail=1; }
  [ "$fail" -eq 0 ] || exit 1
  touch $out
''
