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

    # 3) an unnamed session: identity leads, no empty title line. The model name
    #    always renders, lower-kebab with the opus->opie wink.
    untitled='{"model":{"display_name":"Opus"},"session_id":"s2","workspace":{"current_dir":"/tmp"},"context_window":{"used_percentage":10}}'
    first=$(printf '%s' "$untitled" | bash statusline.sh | strip | sed -n 1p)
    printf 'untitled FIRST=[%s]\n' "$first"
    if ! printf '%s' "$first" | grep -q '◆'; then
      echo "FAIL: unnamed session should lead with the identity line" >&2; exit 1
    fi
    if ! printf '%s' "$first" | grep -q 'opie'; then
      echo "FAIL: model name should always render (opus -> opie)" >&2; exit 1
    fi

    # 3b) the fable -> fabio wink renders too.
    fable='{"model":{"display_name":"Fable 5"},"session_id":"s2b","workspace":{"current_dir":"/tmp"},"context_window":{"used_percentage":10}}'
    fline=$(printf '%s' "$fable" | bash statusline.sh | strip | sed -n 1p)
    printf 'fable FIRST=[%s]\n' "$fline"
    if ! printf '%s' "$fline" | grep -q 'fabio'; then
      echo "FAIL: fable display name should render as fabio" >&2; exit 1
    fi

    # 4) LOCATION dedup: in a real worktree-style repo, the PLACE line shows the
    #    stable repo name (from payload workspace.repo.name), the cwd-relative
    #    subpath, and the branch -- and does NOT emit a separate worktree badge
    #    (branch == worktree name under worktrunk, so it would only duplicate).
    export GIT_CONFIG_GLOBAL=$TMPDIR/gitconfig GIT_CONFIG_SYSTEM=/dev/null
    git config --global init.defaultBranch main
    git config --global user.email t@t
    git config --global user.name t
    git init -q repo
    ( cd repo
      git commit -q --allow-empty -m init
      git config worktrunk.default-branch main
      mkdir -p pkg/thing )
    top=$(git -C repo rev-parse --show-toplevel)
    sub="$top/pkg/thing"

    git -C repo checkout -q -b feat/bar
    loc=$(jq -nc --arg cwd "$sub" '{model:{display_name:"Opus"},session_id:"s6",workspace:{current_dir:$cwd,repo:{name:"mono"}},context_window:{used_percentage:18}}' \
      | bash statusline.sh | strip | sed -n 1p)
    printf 'loc FIRST=[%s]\n' "$loc"
    for want in 'mono' 'pkg/thing' 'feat/bar'; do
      if ! printf '%s' "$loc" | grep -qF "$want"; then
        echo "FAIL: PLACE line missing [$want]" >&2; exit 1
      fi
    done
    if printf '%s' "$loc" | grep -q '⌥'; then
      echo "FAIL: worktree badge should be gone (branch carries that identity)" >&2; exit 1
    fi
    # branch appears exactly once -- no duplicate worktree rendering
    n=$(printf '%s' "$loc" | grep -oF 'feat/bar' | wc -l | tr -d ' ')
    if [ "$n" != "1" ]; then
      echo "FAIL: branch rendered $n times, want 1 (dedup)" >&2; exit 1
    fi

    # 5) on the trunk (branch == worktrunk default-branch), at the repo root:
    #    branch still shows, no subpath segment.
    git -C "$top" checkout -q main
    trunk=$(jq -nc --arg cwd "$top" '{model:{display_name:"Opus"},session_id:"s7",workspace:{current_dir:$cwd,repo:{name:"mono"}},context_window:{used_percentage:5}}' \
      | bash statusline.sh | strip | sed -n 1p)
    printf 'trunk FIRST=[%s]\n' "$trunk"
    if ! printf '%s' "$trunk" | grep -qF 'main'; then
      echo "FAIL: trunk branch not shown" >&2; exit 1
    fi
    if printf '%s' "$trunk" | grep -qF 'pkg/thing'; then
      echo "FAIL: subpath should be empty at repo root" >&2; exit 1
    fi

    # 6) LEDGER + FLEET: seed this session's transcripts (a main turn + one
    #    workflow-agent turn), then assert the spend tape (tokens, dollar adjunct,
    #    churn) and both the quiet fleet tally and the live fleet row.
    sid=led-1
    proj=$HOME/.claude/projects/proj
    mkdir -p "$proj/$sid/subagents/workflows/wf_a"
    tp="$proj/$sid.jsonl"
    ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    jq -nc --arg ts "$ts" '{type:"assistant",timestamp:$ts,requestId:"r1",message:{id:"m1",model:"opus",usage:{input_tokens:1000,output_tokens:2000,cache_read_input_tokens:5000,cache_creation_input_tokens:3000}}}' > "$tp"
    jq -nc --arg ts "$ts" '{type:"assistant",timestamp:$ts,requestId:"r2",message:{id:"m2",model:"opus",usage:{input_tokens:1000,output_tokens:1000,cache_read_input_tokens:1000,cache_creation_input_tokens:1000}}}' > "$proj/$sid/subagents/workflows/wf_a/agent-1.jsonl"
    # ses_tok = 11000 + 4000 = 15000 (15.0K); n_wf=1, n_wfa=1 (one fan-out agent).
    payload=$(jq -nc --arg tp "$tp" --arg sid "$sid" '{model:{display_name:"Opus"},session_id:$sid,transcript_path:$tp,workspace:{current_dir:"/tmp"},cost:{total_cost_usd:4.2,total_lines_added:1234,total_lines_removed:340},context_window:{used_percentage:20}}')

    # 6a) fan-out older than the live window -> quiet tally on the ledger line.
    find "$proj/$sid/subagents" -name '*.jsonl' -exec touch -d '10 minutes ago' {} +
    led=$(printf '%s' "$payload" | STATUSLINE_SYNC=1 bash statusline.sh | strip | grep spend)
    printf 'ledger=[%s]\n' "$led"
    for want in '15.0K' 'tok' '~$4.20' '+1.2K' '-340' '24h' '1 wf'; do
      if ! printf '%s' "$led" | grep -qF -- "$want"; then
        echo "FAIL: ledger line missing [$want]" >&2; exit 1
      fi
    done

    # 6b) fresh fan-out -> the live FLEET row appears (last line).
    rm -rf "$HOME/.claude/.statusline_cache"
    find "$proj/$sid/subagents" -name '*.jsonl' -exec touch {} +
    live=$(printf '%s' "$payload" | STATUSLINE_SYNC=1 bash statusline.sh | strip | tail -1)
    printf 'live=[%s]\n' "$live"
    if ! printf '%s' "$live" | grep -qF 'workflow'; then
      echo "FAIL: live fleet row missing when a workflow agent is fresh" >&2; exit 1
    fi

    mkdir -p $out
    echo "clod statusline check passed" > $out/result
  ''
