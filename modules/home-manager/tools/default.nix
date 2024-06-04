{ pkgs, ... }:
{
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
    niv
  ];
}
