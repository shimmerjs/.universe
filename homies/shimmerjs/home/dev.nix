{ pkgs, lib, ... }:
{
  imports = [
    ../../../modules/home-manager/go.nix
    ./nvim
  ];

  home.packages = with pkgs; [
    cue # Cuelang
    crane # F w containers
    gh # GitHub CLI

    # Build tools
    just
    watchexec
    docker
    gccStdenv
    gcc
    gnumake
    bazelisk

    # Working with diagrams / rendering docs
    pandoc
    graphviz
    d2
    asciinema

    nb
    w3m

    # New tricks?
    eza
    silver-searcher
    fd

    # Nix
    nixfmt

    (python3.withPackages
      # install pip because its not included with the python3 nixpkg by
      # default
      (
        ps: with ps; [
          pip
          pylint
          autopep8
        ]
      )
    )

    # Insert meme here
    kubectl
    kustomize
    kubectx
  ];
}
