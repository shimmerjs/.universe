# Behavioral tests for the clod hook estate, wired by mkchecks.nix into a
# `clod-hooks-<host>` flake check. Four legs:
#
#   go-build-sweep:     must move the stray binary this build dropped into the
#                       gitignored .claude/bin/, and must not touch unrelated
#                       untracked or tracked binaries.
#   gocheck:            the nix-pinned toolchain must actually run with go
#                       absent from PATH (this is the regression test for the
#                       dead `-X <importpath>.goBin` pin, which fail-opened the
#                       gate): a broken module blocks with the real compile
#                       error and keeps the queue; a clean module passes and
#                       drains it; a SubagentStop re-fire must NOT drain the
#                       session-wide queue (a Stop re-fire does).
#   go-fmt-hook:        a parse error blocks (exit 2, basename:line) after
#                       queueing the file; a clean file passes with goimports
#                       rewriting it in place.
#   aw-scriptpath-gate: name= of a deployed workflow is denied with the
#                       scriptPath re-invocation hint; scriptPath, undeployed
#                       names, and pathy names pass through.
#
# Queue files use CLOD_GO_PENDING_DIR (both hook sides honor it) because /tmp
# is not writable inside the build sandbox.
{ pkgs, hooks }:
pkgs.runCommand "clod-hooks-check"
  { nativeBuildInputs = [ pkgs.git pkgs.coreutils pkgs.jq ]; }
  ''
    set -eu
    export HOME=$TMPDIR
    git config --global user.email t@example.com
    git config --global user.name test
    git config --global init.defaultBranch main
    git config --global --add safe.directory '*'

    fail() { echo "FAIL: $1" >&2; exit 1; }

    # leg 1: go-build-sweep narrowing
    repo=$TMPDIR/repo
    mkdir -p "$repo/pkg/foo" "$repo/unrelated"
    cd "$repo"
    git init -q
    printf 'module x\n' > go.mod
    printf 'package foo\n' > pkg/foo/foo.go
    git add go.mod pkg/foo/foo.go
    git commit -qm init

    # an UNRELATED untracked binary elsewhere in the repo -- must be left alone
    cp ${pkgs.coreutils}/bin/true unrelated/debug-bin
    # the stray a bare `go build` drops in the build cwd -- must be swept
    cp ${pkgs.coreutils}/bin/true pkg/foo/foo

    payload=$(jq -cn --arg cwd "$repo/pkg/foo" '{tool_input:{command:"go build"},cwd:$cwd}')
    printf '%s' "$payload" | ${hooks.goBuildSweep}/bin/clod-go-build-sweep || true

    [ ! -e "$repo/pkg/foo/foo" ]   || fail "stray binary was not swept out of the build cwd"
    [ -e "$repo/.claude/bin/foo" ] || fail "stray binary was not relocated into .claude/bin"
    [ -e "$repo/unrelated/debug-bin" ] || fail "an unrelated untracked binary was wrongly moved"

    # a TRACKED binary in the build cwd must never be moved
    cp ${pkgs.coreutils}/bin/true pkg/foo/tracked-bin
    git add -f pkg/foo/tracked-bin
    git commit -qm tracked
    printf '%s' "$payload" | ${hooks.goBuildSweep}/bin/clod-go-build-sweep || true
    [ -e "$repo/pkg/foo/tracked-bin" ] || fail "a tracked binary was wrongly moved"
    echo "go-build-sweep: narrowing OK"

    # leg 2: gocheck pinned-toolchain gate. go must NOT be on PATH here: the
    # whole point is that gocheck's ldflags-pinned goBin does the work.
    if command -v go >/dev/null 2>&1; then
      fail "go leaked onto the check PATH; the pin would go untested"
    fi
    export CLOD_GO_PENDING_DIR=$TMPDIR/queues
    mkdir -p "$CLOD_GO_PENDING_DIR"
    export GOCACHE=$TMPDIR/gocache GOPATH=$TMPDIR/gopath GOTOOLCHAIN=local CGO_ENABLED=0
    gocheck=${hooks.glod}/bin/gocheck

    broken=$TMPDIR/broken
    mkdir -p "$broken"
    printf 'module example.com/broken\n\ngo 1.22\n' > "$broken/go.mod"
    printf 'package b\n\nfunc Bad() int { return "nope" }\n' > "$broken/b.go"
    printf '%s\n' "$broken/b.go" > "$CLOD_GO_PENDING_DIR/go-pending-red"
    res=$(printf '%s' '{"session_id":"red","hook_event_name":"Stop","stop_hook_active":false}' | "$gocheck")
    [ "$(jq -r '.decision // empty' <<<"$res")" = block ] || fail "gocheck did not block a broken module: $res"
    jq -r '.hookSpecificOutput.additionalContext' <<<"$res" | grep -q 'cannot use' \
      || fail "block reason lacks the real compile error (pinned toolchain did not run): $res"
    [ -e "$CLOD_GO_PENDING_DIR/go-pending-red" ] || fail "queue drained on a red gate"

    # SubagentStop: block payload echoes the event; a re-fire keeps the queue
    printf '%s\n' "$broken/b.go" > "$CLOD_GO_PENDING_DIR/go-pending-sub"
    res=$(printf '%s' '{"session_id":"sub","hook_event_name":"SubagentStop","stop_hook_active":false}' | "$gocheck")
    [ "$(jq -r '.hookSpecificOutput.hookEventName // empty' <<<"$res")" = SubagentStop ] \
      || fail "SubagentStop block does not echo its event name: $res"
    printf '%s' '{"session_id":"sub","hook_event_name":"SubagentStop","stop_hook_active":true}' | "$gocheck" >/dev/null
    [ -e "$CLOD_GO_PENDING_DIR/go-pending-sub" ] || fail "SubagentStop re-fire drained the session-wide queue"
    printf '%s' '{"session_id":"sub","hook_event_name":"Stop","stop_hook_active":true}' | "$gocheck" >/dev/null
    [ ! -e "$CLOD_GO_PENDING_DIR/go-pending-sub" ] || fail "Stop re-fire did not drain the queue"

    # clean module: only the main Stop drains. A clean SubagentStop must keep
    # the queue (concurrent editors append during the check window; a subagent
    # drain would drop those lines unchecked). With a dead pin this leg blocks
    # on the fail-closed exec diagnostic, so it pins the toolchain end to end.
    clean=$TMPDIR/clean
    mkdir -p "$clean"
    printf 'module example.com/clean\n\ngo 1.22\n' > "$clean/go.mod"
    printf 'package c\n\nfunc Ok() int { return 1 }\n' > "$clean/ok.go"
    printf '%s\n' "$clean/ok.go" > "$CLOD_GO_PENDING_DIR/go-pending-green"
    res=$(printf '%s' '{"session_id":"green","hook_event_name":"SubagentStop","stop_hook_active":false}' | "$gocheck")
    [ -z "$res" ] || fail "gocheck blocked a clean module on SubagentStop: $res"
    [ -e "$CLOD_GO_PENDING_DIR/go-pending-green" ] || fail "clean SubagentStop drained the session-wide queue"
    res=$(printf '%s' '{"session_id":"green","hook_event_name":"Stop","stop_hook_active":false}' | "$gocheck")
    [ -z "$res" ] || fail "gocheck blocked a clean module: $res"
    [ ! -e "$CLOD_GO_PENDING_DIR/go-pending-green" ] || fail "clean Stop did not drain the queue"
    echo "gocheck: pinned-toolchain gate OK"

    # leg 3: go-fmt-hook syntax gate + queue + in-place goimports
    fmt=$TMPDIR/fmt
    mkdir -p "$fmt"
    printf 'package p\n\nfunc broken( {\n' > "$fmt/bad.go"
    rc=0
    jq -cn --arg f "$fmt/bad.go" '{tool_input:{file_path:$f},session_id:"fmt1"}' \
      | ${hooks.goFmtHook}/bin/clod-go-fmt-hook 2>"$TMPDIR/fmt.err" || rc=$?
    [ "$rc" = 2 ] || fail "parse error did not block the edit (rc=$rc)"
    grep -q 'bad.go:' "$TMPDIR/fmt.err" || fail "block message lacks basename:line: $(cat "$TMPDIR/fmt.err")"
    grep -qx "$fmt/bad.go" "$CLOD_GO_PENDING_DIR/go-pending-fmt1" || fail "edited file was not queued for the Stop pass"

    printf 'package p\n\nfunc F() { fmt.Println("x") }\n' > "$fmt/ok.go"
    jq -cn --arg f "$fmt/ok.go" '{tool_input:{file_path:$f},session_id:"fmt1"}' \
      | ${hooks.goFmtHook}/bin/clod-go-fmt-hook || fail "clean file was blocked"
    grep -q '"fmt"' "$fmt/ok.go" || fail "goimports did not rewrite the file in place"

    # non-.go and sessionless inputs fall through untouched
    printf '%s' '{"tool_input":{"file_path":"/x.txt"},"session_id":"fmt1"}' | ${hooks.goFmtHook}/bin/clod-go-fmt-hook \
      || fail "non-.go input did not pass through"
    echo "go-fmt-hook: syntax gate + queue OK"

    # leg 4: aw-scriptpath-gate
    mkdir -p "$HOME/.claude/workflows"
    touch "$HOME/.claude/workflows/aw-foo.js"
    gate=${hooks.awScriptpathGate}/bin/clod-aw-scriptpath-gate
    rc=0
    printf '%s' '{"tool_input":{"name":"aw-foo"}}' | "$gate" 2>"$TMPDIR/gate.err" || rc=$?
    [ "$rc" = 2 ] || fail "deployed-workflow name= was not denied (rc=$rc)"
    grep -q 'scriptPath' "$TMPDIR/gate.err" || fail "deny message lacks the scriptPath re-invocation hint"
    grep -q 'aw-foo.js' "$TMPDIR/gate.err" || fail "deny message lacks the deployed path"
    printf '%s' '{"tool_input":{"name":"aw-foo","scriptPath":"/anywhere/aw-foo.js"}}' | "$gate" \
      || fail "scriptPath invocation was wrongly denied"
    printf '%s' '{"tool_input":{"name":"not-deployed"}}' | "$gate" \
      || fail "undeployed name was wrongly denied"
    printf '%s' '{"tool_input":{"name":"plugin/wf"}}' | "$gate" \
      || fail "pathy (plugin) name was wrongly denied"
    echo "aw-scriptpath-gate: deny/pass narrowing OK"

    touch "$out"
  ''
