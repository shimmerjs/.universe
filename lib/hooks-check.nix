# Behavioral test for the go-build-sweep PostToolUse hook. It must move the stray
# binary THIS build dropped (the build cwd's default output, or an -o target) into
# the gitignored .claude/bin/, and must NOT touch an unrelated untracked binary
# elsewhere in the tree, nor a tracked binary. Wired by mkchecks.nix into a
# `clod-hooks-<host>` flake check.
{ pkgs, goBuildSweep }:
pkgs.runCommand "clod-hooks-check"
  { nativeBuildInputs = [ pkgs.git pkgs.coreutils pkgs.jq ]; }
  ''
    set -eu
    export HOME=$TMPDIR
    git config --global user.email t@example.com
    git config --global user.name test
    git config --global init.defaultBranch main
    git config --global --add safe.directory '*'

    repo=$TMPDIR/repo
    mkdir -p "$repo/pkg/foo" "$repo/unrelated"
    cd "$repo"
    git init -q
    printf 'module x\n' > go.mod
    printf 'package foo\n' > pkg/foo/foo.go
    git add go.mod pkg/foo/foo.go
    git commit -qm init

    fail() { echo "FAIL: $1" >&2; exit 1; }

    # an UNRELATED untracked binary elsewhere in the repo -- must be left alone
    cp ${pkgs.coreutils}/bin/true unrelated/debug-bin
    # the stray a bare `go build` drops in the build cwd -- must be swept
    cp ${pkgs.coreutils}/bin/true pkg/foo/foo

    payload=$(jq -cn --arg cwd "$repo/pkg/foo" '{tool_input:{command:"go build"},cwd:$cwd}')
    printf '%s' "$payload" | ${goBuildSweep}/bin/clod-go-build-sweep || true

    [ ! -e "$repo/pkg/foo/foo" ]   || fail "stray binary was not swept out of the build cwd"
    [ -e "$repo/.claude/bin/foo" ] || fail "stray binary was not relocated into .claude/bin"
    [ -e "$repo/unrelated/debug-bin" ] || fail "an unrelated untracked binary was wrongly moved"

    # a TRACKED binary in the build cwd must never be moved
    cp ${pkgs.coreutils}/bin/true pkg/foo/tracked-bin
    git add -f pkg/foo/tracked-bin
    git commit -qm tracked
    printf '%s' "$payload" | ${goBuildSweep}/bin/clod-go-build-sweep || true
    [ -e "$repo/pkg/foo/tracked-bin" ] || fail "a tracked binary was wrongly moved"

    echo "go-build-sweep: narrowing OK (cwd stray swept; unrelated + tracked untouched)"
    touch "$out"
  ''
