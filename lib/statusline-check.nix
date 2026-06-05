# Smoke-tests a clod statusline script: bash syntax, plus the session title
# renders on its own first line (lower kebab) and never bleeds onto the identity
# line. Wired by mkchecks.nix into a `clod-statusline-<host>` flake check.
{ pkgs, statuslineScript }:
pkgs.runCommandLocal "clod-statusline-check"
  {
    nativeBuildInputs = [
      pkgs.bash
      pkgs.jq
      pkgs.gawk
      pkgs.coreutils
      pkgs.findutils
      pkgs.gnused
      pkgs.gnugrep
      pkgs.git
    ];
    inherit statuslineScript;
  }
  ''
    set -eo pipefail
    export HOME=$TMPDIR
    export STATUSLINE_SYNC=1

    cp "$statuslineScript" statusline.sh

    # 1) syntax must be clean
    bash -n statusline.sh

    # drop SGR color escapes so we can assert on plain text
    strip() { sed -E 's/\x1b\[[0-9;]*m//g'; }

    # 2) a Claude-named session: title on its OWN first line, lower kebab, and
    #    the identity (model) line must NOT carry the title.
    titled='{"model":{"display_name":"Opus"},"session_id":"s1","session_name":"My Cool Chat Title","workspace":{"current_dir":"/tmp"},"context_window":{"used_percentage":42}}'
    rendered=$(printf '%s' "$titled" | bash statusline.sh | strip)
    l1=$(printf '%s\n' "$rendered" | sed -n 1p)
    l2=$(printf '%s\n' "$rendered" | sed -n 2p)
    printf 'titled L1=[%s]\ntitled L2=[%s]\n' "$l1" "$l2"
    if ! printf '%s' "$l1" | grep -q 'my-cool-chat-title'; then
      echo "FAIL: title not rendered lower-kebab on its own first line" >&2; exit 1
    fi
    if printf '%s' "$l1" | grep -q '◆'; then
      echo "FAIL: identity bled onto the title line" >&2; exit 1
    fi
    if ! printf '%s' "$l2" | grep -q '◆'; then
      echo "FAIL: identity line missing right after the title" >&2; exit 1
    fi
    if printf '%s' "$l2" | grep -q 'my-cool-chat-title'; then
      echo "FAIL: title duplicated onto the identity line" >&2; exit 1
    fi

    # 3) an unnamed session: identity leads, no empty title line.
    untitled='{"model":{"display_name":"Opus"},"session_id":"s2","workspace":{"current_dir":"/tmp"},"context_window":{"used_percentage":10}}'
    first=$(printf '%s' "$untitled" | bash statusline.sh | strip | sed -n 1p)
    printf 'untitled FIRST=[%s]\n' "$first"
    if ! printf '%s' "$first" | grep -q '◆'; then
      echo "FAIL: unnamed session should lead with the identity line" >&2; exit 1
    fi

    mkdir -p $out
    echo "clod statusline check passed" > $out/result
  ''
