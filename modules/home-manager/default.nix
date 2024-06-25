# Default configuration for home-manager, intended to be used by any hosts that
# use home-manager, regardless of OS.
# 
# Basically the minimal common set of configuration for "shimmerjs", whoever 
# that may be.
#
# Should not assume graphical environment
{ pkgs, ... }:
{
  imports = [
    ./git.nix
    ./go.nix
    ./zsh.nix
  ];

  # Configuration for home-manager itself
  programs.home-manager.enable = true;
  home.stateVersion = "21.03"; # Version set by home-manager when first installed.

  programs.ssh.enable = true;

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

    cue # Cuelang
    crane # F w containers
    gh # GitHub CLI

    # Working with diagrams / rendering docs
    pandoc
    graphviz
    d2
    asciinema

    # Build tools
    just
    watchexec
    docker
    gccStdenv
    gcc
    gnumake
    bazelisk

    # Languages and language tooling
    yarn
    # https://github.com/microsoft/vscode-remote-release/issues/648#issuecomment-503148523
    nodejs-18_x
    nixpkgs-fmt
    (python3.withPackages
      # install pip because its not included with the python3 nixpkg by
      # default
      (ps: with ps; [ pip pylint autopep8 ]))

    # Insert meme here
    kubectl
    kustomize
    kubectx
  ];
}
