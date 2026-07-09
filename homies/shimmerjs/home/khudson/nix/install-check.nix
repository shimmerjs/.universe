# Flake check for the activation install script: exercises the SAME
# derivation module.nix's khudsonBinInstall / khudsonTouchdInstall run
# (install-script.nix), against a scratch dir -- no TCC, no launchd. Four
# legs:
#   1. fresh dir            -> reinstall branch runs, stamp written, marker
#                              touched
#   2. signed + fresh stamp -> fast path, binary untouched, no marker
#   3. tampered install     -> verify fails (stamp still matches, so ONLY
#      (one byte appended)     the per-activation verify can catch it), the
#                              loud stderr log names the path, and the
#                              reinstall branch re-runs
#   4. mode-tampered install -> -x guard catches it (codesign verify passes
#      (chmod 644)              on a non-executable), reinstall repairs
# Signing uses the real /usr/bin/codesign with the ad-hoc identity `-`:
# darwin builds run sandbox-off here (see the doCheck comment in module.nix),
# so /usr/bin is reachable and ad-hoc signing needs no keychain.
# --verify --strict rejects an appended byte ("main executable
# failed strict validation"). KHUDSON_CODESIGN stays the spec-authorized
# mock fallback if this build environment ever loses codesign.
{ pkgs }:
let
  installScript = import ./install-script.nix { inherit pkgs; };
in
pkgs.runCommand "khudson-install-check"
  {
    nativeBuildInputs = [ installScript ];
  }
  ''
    root=$PWD/scratch
    mkdir -p "$root/bin"
    src=${pkgs.coreutils}/bin/true
    installPath=$root/bin/khudson-under-test
    stamp=$root/bin/.khudson-under-test.store-path
    want="${pkgs.coreutils} sign-v1:-"
    marker=$root/.updated

    runInstall() {
      khudson-bin-install "$src" "$installPath" "$stamp" "$want" - "$marker"
    }

    echo "leg 1: fresh dir -> reinstall branch"
    runInstall 2>leg1.log
    [ -x "$installPath" ] || { echo "FAIL: no binary installed"; exit 1; }
    [ "$(cat "$stamp")" = "$want" ] || { echo "FAIL: stamp not written"; exit 1; }
    [ -e "$marker" ] || { echo "FAIL: updated marker not touched"; exit 1; }
    if grep -q "FAILED codesign" leg1.log; then
      echo "FAIL: fresh install logged a verify failure"; exit 1
    fi
    /usr/bin/codesign --verify --strict "$installPath" || {
      echo "FAIL: fresh install does not verify"; exit 1; }
    rm -f "$marker"

    echo "leg 2: signed install + matching stamp -> fast path"
    cp "$installPath" "$installPath.before"
    runInstall 2>leg2.log
    cmp "$installPath" "$installPath.before" || {
      echo "FAIL: fast path rewrote the binary"; exit 1; }
    [ ! -e "$marker" ] || { echo "FAIL: fast path touched the marker"; exit 1; }

    echo "leg 3: tampered install -> loud verify failure + reinstall"
    printf X >> "$installPath"
    runInstall 2>leg3.log
    grep -q "FAILED codesign" leg3.log || {
      echo "FAIL: tamper produced no loud log"; cat leg3.log; exit 1; }
    grep -Fq "$installPath" leg3.log || {
      echo "FAIL: loud log does not name the path"; cat leg3.log; exit 1; }
    [ -e "$marker" ] || {
      echo "FAIL: tamper reinstall did not touch the marker"; exit 1; }
    /usr/bin/codesign --verify --strict "$installPath" || {
      echo "FAIL: reinstall branch did not repair the signature"; exit 1; }

    echo "leg 4: mode-tampered install (chmod 644) -> repaired"
    # codesign verify passes on a non-executable file; only the -x guard
    # catches it, and KeepAlive would relaunch the broken daemon forever.
    # NOTE the identity-pinned verify branch (identity
    # != "-") has no build-time leg: ad-hoc "-" carries no cert to pin,
    # and a real cert does not exist in the sandbox -- it is exercised on
    # every deployed activation, where a pin failure logs loudly.
    rm -f "$marker"
    chmod 644 "$installPath"
    runInstall 2>leg4.log
    [ -x "$installPath" ] || {
      echo "FAIL: mode tamper not repaired"; exit 1; }
    [ -e "$marker" ] || {
      echo "FAIL: mode-tamper reinstall did not touch the marker"; exit 1; }

    touch $out
  ''
