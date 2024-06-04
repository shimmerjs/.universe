{ pkgs, ... }:
{
  programs.home-manager.enable = true;

  # Version set by home-manager when first installed.
  home.stateVersion = "21.03";
}
