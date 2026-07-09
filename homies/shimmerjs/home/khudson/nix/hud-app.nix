# Rebranded kitty.app for the HUD instance: Dock/Cmd-Tab identity comes from
# the .app bundle enclosing the RUNNING executable image, so a real copy named
# khudson.app (directory name is load-bearing for display-name honoring) with
# a patched Info.plist + own icns shows "khudson" instead of kitty. Consumers
# must exec the copy's Contents/MacOS/.kitty-wrapped -- Contents/MacOS/kitty
# is a makeBinaryWrapper C shim that execv's the ORIGINAL store path's
# .kitty-wrapped, snapping identity back to kitty -- and the bundle must be a
# copy, never a symlinked executable (kitty realpaths itself). No re-signing:
# the store bundle has no bundle seal, only per-Mach-O ad-hoc signatures whose
# CDHashes are path-independent, and store artifacts carry no quarantine xattr.
{
  stdenvNoCC,
  kitty,
  imagemagick,
  libicns,
  python3,
}:
let
  # The icon is a CHECKED-IN asset (khudson-icon-1024.png beside this
  # file), rendered out-of-tree with imagemagick + Menlo and committed as
  # a PNG because the build sandbox has no fonts -- the derivation only
  # resizes and packs.
  # png2icns has no icns type for some sizes (64 and 1024 are the likely
  # rejects); those drop rather than fail the build, with at least one
  # >=256px element as the floor (self-checked below).
  khudson-hud-icon = stdenvNoCC.mkDerivation {
    pname = "khudson-hud-icon";
    version = "0.1.0";
    nativeBuildInputs = [
      imagemagick
      libicns
    ];
    dontUnpack = true;
    buildCommand = ''
      magick ${./khudson-icon-1024.png} PNG32:master.png
      accepted=
      for s in 16 32 64 128 256 512 1024; do
        magick master.png -resize "''${s}x''${s}" "PNG32:icon-$s.png"
        if png2icns "probe-$s.icns" "icon-$s.png" > /dev/null 2>&1; then
          accepted="$accepted icon-$s.png"
        else
          echo "png2icns rejected ''${s}px, dropping that size" >&2
        fi
      done
      png2icns "$out" $accepted
      # self-check: icns magic + at least one >=256px element landed
      [ "$(head -c 4 "$out")" = icns ]
      icns2png -l "$out" | grep -Eq '256x256|512x512|1024x1024'
    '';
  };
in
stdenvNoCC.mkDerivation {
  pname = "khudson-hud-app";
  version = kitty.version;
  nativeBuildInputs = [ python3 ];
  dontUnpack = true;
  buildCommand = ''
    mkdir -p "$out/Applications"
    # TRAP for future callers: the copied Contents/MacOS/kitty is nixpkgs'
    # makeCWrapper shim exec-ing the ORIGINAL store bundle's .kitty-wrapped
    # (and Resources/kitty/kitty/launcher symlinks resolve to it) -- launch
    # the bundle any way other than a direct exec of THIS copy's
    # .kitty-wrapped (Finder, open -a, re-exec via MacOS/kitty) and the
    # process image, and therefore the Dock identity, snaps back to kitty.
    # Only the hud agent's direct .kitty-wrapped exec carries the rebrand.
    cp -R "${kitty}/Applications/kitty.app" "$out/Applications/khudson.app"
    chmod -R u+w "$out/Applications/khudson.app"
    app="$out/Applications/khudson.app"

    # the asset catalog would shadow the icns; the quick-access sub-app is
    # unused by the HUD
    rm "$app/Contents/Resources/Assets.car"
    rm -r "$app/Contents/kitty-quick-access.app"
    install -m 644 ${khudson-hud-icon} "$app/Contents/Resources/khudson.icns"

    # plistlib, not /usr/bin/plutil (absent from the build sandbox)
    python3 - "$app/Contents/Info.plist" << 'PYEOF'
    import plistlib
    import sys

    path = sys.argv[1]
    with open(path, "rb") as f:
        d = plistlib.load(f)
    d["CFBundleName"] = "khudson"
    d["CFBundleDisplayName"] = "khudson"
    d["CFBundleIdentifier"] = "org.khudson.hud"
    d["CFBundleIconFile"] = "khudson.icns"
    for k in ("CFBundleIconName", "CFBundleDocumentTypes",
              "CFBundleURLTypes", "NSServices"):
        d.pop(k, None)
    with open(path, "wb") as f:
        plistlib.dump(d, f)
    PYEOF

    # self-checks: fail the BUILD, not runtime
    python3 - "$app" << 'PYEOF'
    import os
    import plistlib
    import sys

    app = sys.argv[1]
    with open(os.path.join(app, "Contents/Info.plist"), "rb") as f:
        d = plistlib.load(f)
    want = {
        "CFBundleName": "khudson",
        "CFBundleDisplayName": "khudson",
        "CFBundleIdentifier": "org.khudson.hud",
        "CFBundleIconFile": "khudson.icns",
    }
    for k, v in want.items():
        assert d.get(k) == v, (k, d.get(k))
    for k in ("CFBundleIconName", "CFBundleDocumentTypes",
              "CFBundleURLTypes", "NSServices"):
        assert k not in d, k
    wrapped = os.path.join(app, "Contents/MacOS/.kitty-wrapped")
    assert os.path.isfile(wrapped), wrapped
    with open(wrapped, "rb") as f:
        magic = f.read(4)
    mach_o = (b"\xcf\xfa\xed\xfe", b"\xce\xfa\xed\xfe",
              b"\xca\xfe\xba\xbe", b"\xca\xfe\xba\xbf")
    assert magic in mach_o, magic
    assert os.path.isfile(os.path.join(app, "Contents/Resources/khudson.icns"))
    assert not os.path.exists(os.path.join(app, "Contents/Resources/Assets.car"))
    PYEOF
  '';
}
