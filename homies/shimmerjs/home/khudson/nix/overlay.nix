# khudson kitty overlay. INERT: not referenced by any nixpkgs.overlays yet.
# Applies kitty-menubar.patch so a --edge=center panel covers the full display
# height, menu bar strip included (stock kitty subtracts menubar_height,
# leaving a dead strip at the top of the Edge; panels draw at
# NSPopUpMenuWindowLevel - 1, above the bar, so full coverage is safe).
#
# Wiring: add alongside the qemu/claude-code overlays in
# modules/darwin/default.nix nixpkgs.overlays, or scope it to the Edge host.
# Every kitty consumer then rebuilds from source (patched derivation is a
# cache miss); programs.kitty, the substrate/hud agents (the hud via the khudson.app
# copy, hud-app.nix), and the daily kitty all
# pick it up from the one pkgs.kitty.
#
# Codesign-hook compatibility (../../kitty/default.nix signKitty): unaffected.
# That hook re-signs "$HOME/Applications/Home Manager Apps/kitty.app" after
# linkGeneration, i.e. whatever kitty the generation links -- patched or not.
# The patch predates signing, so the ad-hoc re-sign covers the patched Mach-O
# the same way it covers stock. The substrate agent execs the store
# kitty binary directly (not the LS bundle); AMFI behavior on that path is a
# spike 3 exit criterion.
#
# Patch is pinned to kitty 0.47.4 sources; a version bump that drifts
# glfw/cocoa_window.m fails the kitty build at patch time (loud, at switch),
# and the khudson RC client pin [0,47,4] has to move with it.
final: prev: {
  kitty = prev.kitty.overrideAttrs (old: {
    patches = (old.patches or [ ]) ++ [ ./kitty-menubar.patch ];
  });
}
