# Verify + reinstall for the out-of-store TCC-granted binaries (khudson and
# touchd). Single source of truth: module.nix's khudsonBinInstall /
# khudsonTouchdInstall activation steps exec this script, and
# install-check.nix exercises the very same derivation against a scratch
# dir, so what the check proves is exactly what activation runs.
#
# Args: SRC_BIN INSTALL_PATH STAMP_PATH WANT_STRING IDENTITY [UPDATED_MARKER]
#   SRC_BIN        store-built binary to install
#   INSTALL_PATH   fixed out-of-store path the TCC grant keys on
#   STAMP_PATH     store-path + recipe stamp beside the install
#   WANT_STRING    the stamp content that means "current"
#   IDENTITY       codesign identity (login-keychain cert, or `-` for ad-hoc)
#   UPDATED_MARKER optional: touched iff a reinstall happened (touchd passes
#                  its .touchd-updated so khudsonRestart kickstarts only on
#                  change; khudson passes none -- its agents restart anyway)
#
# Env: KHUDSON_CODESIGN overrides /usr/bin/codesign (install-check.nix uses
# it if the real codesign is unusable in the build).
{ pkgs }:
pkgs.writeShellApplication {
  name = "khudson-bin-install";
  runtimeInputs = [ pkgs.coreutils ];
  text = ''
    srcBin="$1"
    installPath="$2"
    stampPath="$3"
    want="$4"
    identity="$5"
    updatedMarker="''${6:-}"
    codesign="''${KHUDSON_CODESIGN:-/usr/bin/codesign}"

    needsInstall=false
    # -x, not -e: a mode-tampered binary (chmod 644) passes codesign verify
    # but KeepAlive would relaunch a non-executable forever
    if [ -x "$installPath" ]; then
      # Signature verify on EVERY activation: the stamp below is only the
      # store-path/recipe fast-path, never a verify bypass. A TCC-granted
      # binary failing verification is the nightmare case -- KeepAlive would
      # relaunch it with the grant dead -- so it is loud, then repaired.
      # The verify PINS the signing identity when one exists: a plain
      # --verify accepts ANY valid signature, so an attacker re-signing a
      # swapped binary ad-hoc would pass silently while the TCC grant
      # (keyed on the identity) dies. Ad-hoc "-" has no
      # cert to pin; the plain verify stands there (check environments).
      verifyArgs=(--verify --strict)
      if [ "$identity" != "-" ]; then
        verifyArgs+=(-R="certificate leaf[subject.CN] = \"$identity\"")
      fi
      if ! "$codesign" "''${verifyArgs[@]}" "$installPath" 2>/dev/null; then
        echo "khudson: $installPath FAILED codesign signature verification (identity-pinned) -- the TCC-granted binary is tampered, unsigned, or re-signed by someone else; re-installing and re-signing" >&2
        needsInstall=true
      fi
    else
      needsInstall=true
    fi
    if [ "$(cat "$stampPath" 2>/dev/null || true)" != "$want" ]; then
      needsInstall=true
    fi

    if [ "$needsInstall" = true ]; then
      # stage + sign BESIDE the granted binary, replace atomically only
      # after codesign succeeds: signing the install path in place means a
      # codesign failure (identity missing on a fresh host, expired cert)
      # leaves an unsigned copy where the TCC-granted binary was, and
      # KeepAlive relaunches it with the grant dead -- the exact failure
      # the fixed-path install exists to prevent
      install -m 755 "$srcBin" "$installPath.next"
      "$codesign" --force --sign "$identity" "$installPath.next"
      mv -f "$installPath.next" "$installPath"
      printf %s "$want" > "$stampPath"
      if [ -n "$updatedMarker" ]; then
        touch "$updatedMarker"
      fi
    fi
  '';
}
