# kitty with nixpkgs' PATH wrapper moved out of the .app bundle.
#
# nixpkgs wraps kitty by dropping a makeBinaryWrapper C shim at
# Applications/kitty.app/Contents/MacOS/kitty -- the CFBundleExecutable -- which
# suffixes PATH and execs the real Mach-O, renamed .kitty-wrapped beside it. On
# macOS 27 that post-launch change of process image breaks the LaunchServices
# process binding: any LS-launched kitty (Dock, Finder, `open`, loginwindow
# resume) comes back from NSRunningApplication with processIdentifier == -1 and
# isTerminated() == false. Rectangle feeds that -1 to
# AXUIElementCreateApplication, gets no element back, and beeps -- window
# management is dead for kitty and kitty alone. A shell-exec'd kitty gets a real
# pid and Rectangle drives it fine; the discriminator is the LS launch.
#
# So: the shim moves to $out/bin/kitty (the CLI keeps its PATH suffix), the
# bundle's CFBundleExecutable becomes the real binary, and LSEnvironment in
# Info.plist hands LS-launched kitty the PATH the shim used to build --
# /usr/bin:/bin:/usr/sbin:/sbin is what launchd gives a GUI app here, and the
# suffix is appended exactly as wrapProgram appended it. .kitty-wrapped stays
# behind as a relative symlink so the shim's hardcoded exec target still
# resolves, and so does khudson's launcher, which execs that path inside its own
# rebranded copy of the bundle to own the Dock identity.
#
# Preserve KITTY_LAUNCHED_BY_LAUNCH_SERVICES in LSEnvironment: kitty needs it
# to read macos-launch-services-cmdline and start in ~. Replacing the dict
# dropped --listen-on and left windows without an explicit cwd in /.
#
# No re-signing: the bundle carries no seal, only per-Mach-O ad-hoc signatures
# whose CDHashes are path-independent.
#
# Verified 2026-09-23 on macOS 27.0 (26A428) against a hand-built copy of the
# bundle: unwrapped and Finder-launched it reports a real pid and Rectangle's
# left-half moves the window; the wrapped instance reports -1 in the same run.
# Drop this overlay when nixpkgs stops wrapping the bundle executable -- the
# build fails loudly if it does.
{ lib, ... }:
{
  nixpkgs.overlays = [
    (final: prev: {
      kitty = prev.kitty.overrideAttrs (old: {
        postFixup =
          (old.postFixup or "")
          + lib.optionalString prev.stdenv.hostPlatform.isDarwin (
            let
              # Mirrors the wrapProgram call in nixpkgs' kitty installPhase. The
              # grep below fails the build if the two ever drift.
              suffix = lib.makeBinPath [
                final.imagemagick
                final.ncurses.dev
              ];
            in
            ''
              macos="$out/Applications/kitty.app/Contents/MacOS"
              shim="$macos/kitty"
              real="$macos/.kitty-wrapped"

              if [ ! -f "$real" ]; then
                echo "kitty-overlay: no .kitty-wrapped in the bundle -- nixpkgs stopped wrapping the app executable, drop modules/darwin/kitty-overlay.nix" >&2
                exit 1
              fi
              if ! grep -q "$out/bin:${suffix}" "$shim"; then
                echo "kitty-overlay: the shim's PATH suffix is not '$out/bin:${suffix}' -- nixpkgs changed the wrapProgram call, update the suffix here" >&2
                exit 1
              fi

              rm "$out/bin/kitty"
              mv "$shim" "$out/bin/kitty"
              mv "$real" "$shim"
              ln -s kitty "$real"

              ${final.python3.interpreter} - "$out/Applications/kitty.app/Contents/Info.plist" <<PYEOF
              import plistlib
              import sys

              path = sys.argv[1]
              with open(path, "rb") as f:
                  d = plistlib.load(f)
              d.setdefault("LSEnvironment", {})["PATH"] = "/usr/bin:/bin:/usr/sbin:/sbin:$out/bin:${suffix}"
              with open(path, "wb") as f:
                  plistlib.dump(d, f)
              PYEOF
            ''
          );
      });
    })
  ];
}
