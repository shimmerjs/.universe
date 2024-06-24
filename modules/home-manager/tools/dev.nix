{ pkgs, ... }:
{
  imports = [
    ./go.nix
  ];

  home.packages = with pkgs; [
    niv # Manages pinned dependencies for Nix projects

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

    gh
  ];
}
