# TODO: no longer need this with flakes...?
{ sources ? import ./sources.nix }:
import sources.nixpkgs {
  overlays = [
    (_: pkgs: {
      inherit sources;
      fcitx-engines = pkgs.fcitx;
    })
  ];
  config = { };
}
