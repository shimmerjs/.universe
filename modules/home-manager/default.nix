# Default configuration for home-manager, intended to be used by any hosts that
# use home-manager, regardless of OS.
#
# Should not assume graphical environment
{ pkgs, ... }:
{
  # Configuration for home-manager itself
  programs.home-manager.enable = true;
  home.stateVersion = "21.03"; # Version set by home-manager when first installed.

  home.sessionPath = [
    "$HOME/.local/bin"
  ];

  programs.ssh.enable = true;

  # Packages that should be everywhere, basically.
  home.packages = with pkgs; [
    curl
    fzf
    bat
    htop
    jq
    coreutils
    ack
    tree
    vim
  ];
}
