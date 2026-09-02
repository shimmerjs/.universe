# Guards the weekly macOS GC job (modules/darwin/nix-gc.nix):
#   - the dead-path filter holds back real .app bundles and NOTHING else, over a
#     fixture tree that mirrors the store shapes involved (a VS Code closure, a
#     home-manager Applications/ symlink farm, a plain path);
#   - the collect script still routes its dead list through that filter;
#   - the daemon keeps the posture that makes failures visible, and the builtin
#     nix-collect-garbage collector stays off.
# Wired by mkchecks.nix into a `nix-gc-<host>` flake check.
#   - the manual force collector shares that filter and keeps the properties
#     that separate it from the weekly job.
{
  pkgs,
  collect,
  force,
  gcAutomatic,
  daemonJson,
}:
pkgs.runCommandLocal "nix-gc-check"
  {
    nativeBuildInputs = [
      pkgs.bash
      pkgs.coreutils
      pkgs.gnugrep
      pkgs.jq
    ];
    inherit collect force gcAutomatic;
    passAsFile = [ "daemonJson" ];
    inherit daemonJson;
  }
  ''
    set -eo pipefail

    # 1) the collect script must still call the filter -- dropping it is how the
    #    wedge comes back. Take the partition path from the script itself so the
    #    fixtures below exercise the deployed artifact.
    partition=$(grep -o '/nix/store/[^ "]*-nix-gc-partition/bin/nix-gc-partition' "$collect" | head -1)
    if [ -z "$partition" ]; then
      echo "FAIL: $collect does not invoke nix-gc-partition" >&2; exit 1
    fi
    grep -q 'nix-store --delete' "$collect" || { echo "FAIL: collect script never deletes" >&2; exit 1; }

    # 1b) the two properties that keep the delete from failing "still alive":
    #     the held-back bundles' own dependencies come out of the list, and the
    #     delete runs with the keep-* hops off so requisites is the complete
    #     exclusion. Dropping either re-wedges the sweep the first time a dead
    #     bundle has dead deps.
    grep -q 'nix-store -q --requisites' "$collect" || {
      echo "FAIL: collect script does not subtract the held-back closure" >&2; exit 1; }
    grep -q 'keep-outputs false' "$collect" && grep -q 'keep-derivations false' "$collect" || {
      echo "FAIL: delete must run with keep-outputs/keep-derivations off" >&2; exit 1; }
    grep -q "Cannot delete path" "$collect" || {
      echo "FAIL: collect script does not recover from paths that go live mid-run" >&2; exit 1; }
    # root's $HOME hides the user profiles from nix's own discovery, so they have
    # to be reached by absolute path or their generations pin closures forever
    grep -q '/Users/\*/.local/state/nix/profiles' "$collect" || {
      echo "FAIL: collect script does not prune user profiles by absolute path" >&2; exit 1; }

    # 1c) the manual force collector. It must share the hazard filter (a force
    #     run hits the same TCC wall), and keep the three properties that make
    #     it a force run rather than a second weekly one: enumerate with the
    #     keep-* hops off, feed the list over --stdin, and do nothing without
    #     --yes. --stdin is load-bearing, not stylistic: the force list is
    #     several MB of argv against a 1 MB kern.argmax, and `nix-store
    #     --delete` given no path ARGUMENTS ignores stdin, deletes nothing and
    #     exits 0 -- a silent no-op that reads as a clean run.
    if [ -z "$force" ]; then
      echo "FAIL: host schedules nix-gc-weekly but has no nix-gc-force on PATH" >&2; exit 1
    fi
    grep -q "$partition" "$force" || { echo "FAIL: $force does not invoke nix-gc-partition" >&2; exit 1; }
    grep -q 'nix-store -q --requisites' "$force" || {
      echo "FAIL: force script does not subtract the held-back closure" >&2; exit 1; }
    grep -q -- '--print-dead' "$force" && grep -q 'keep-outputs false' "$force" || {
      echo "FAIL: force script must enumerate dead paths with the keep-* hops off" >&2; exit 1; }
    grep -q -- 'nix-store --delete --stdin' "$force" || {
      echo "FAIL: force script must pass paths over --stdin (argv overflows, and no --stdin is a silent no-op)" >&2; exit 1; }
    grep -q 'apply=0' "$force" && grep -q -- '--yes' "$force" || {
      echo "FAIL: force script must default to a dry run" >&2; exit 1; }

    # 2) fixture store shapes.
    mkdir -p store
    # the poisoned shape: a real bundle directory (name carries spaces, as the
    # VS Code one does)
    mkdir -p "store/p-bundle/Applications/Visual Studio Code.app/Contents/MacOS"
    # a real bundle deeper than the Applications/ convention
    mkdir -p store/p-nested/libexec/foo/Helper.app/Contents
    # home-manager-applications / darwin-system shape: Applications/ full of
    # SYMLINKS to bundles. Deleting these is the whole point of the sweep.
    mkdir -p store/p-farm/Applications
    ln -s "$PWD/store/p-bundle/Applications/Visual Studio Code.app" \
      "store/p-farm/Applications/Visual Studio Code.app"
    ln -s /nonexistent/Dangling.app store/p-farm/Applications/Dangling.app
    # plain path, plus a regular FILE that merely ends in .app
    mkdir -p store/p-plain/bin
    touch store/p-plain/bin/thing.app
    # a derivation: the force run's dead list is mostly these, and the filter
    # short-circuits them without a find. Must still land in collectible.
    touch store/p-thing.drv

    # p-gone raced away between enumerate and delete
    for p in p-bundle p-nested p-farm p-plain p-gone p-thing.drv; do
      echo "$PWD/store/$p"
    done > dead

    "$partition" dead held collect.txt

    check() {
      local label=$1 file=$2
      shift 2
      printf '%s\n' "$@" | sed "s|^|$PWD/store/|" > want
      if ! diff -u want "$file"; then
        echo "FAIL: $label partition mismatch" >&2; exit 1
      fi
    }
    check "held-back" held p-bundle p-nested
    check "collectible" collect.txt p-farm p-plain p-gone p-thing.drv

    # 3) daemon posture: a silent failure is what made this bug invisible.
    d=$(cat "$daemonJsonPath")
    logout=$(printf '%s' "$d" | jq -r '.StandardOutPath // ""')
    logerr=$(printf '%s' "$d" | jq -r '.StandardErrorPath // ""')
    if [ -z "$logout" ] || [ "$logout" != "$logerr" ]; then
      echo "FAIL: daemon needs StandardOutPath == StandardErrorPath, got [$logout] [$logerr]" >&2; exit 1
    fi
    prog=$(printf '%s' "$d" | jq -r '.ProgramArguments[0] // ""')
    if [ "$prog" != "$collect" ]; then
      echo "FAIL: daemon runs [$prog], want [$collect]" >&2; exit 1
    fi
    if [ "$(printf '%s' "$d" | jq -r '.StartCalendarInterval.Weekday // "none"')" = "none" ]; then
      echo "FAIL: daemon has no weekly schedule" >&2; exit 1
    fi

    # 4) two collectors means the broken one still wedges weekly.
    if [ "$gcAutomatic" != "false" ]; then
      echo "FAIL: nix.gc.automatic must stay off on darwin (nix-collect-garbage aborts on .app bundles)" >&2; exit 1
    fi

    mkdir -p $out
    echo "nix-gc check passed" > $out/result
  ''
