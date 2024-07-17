{ pkgs, lib, ... }:
{
  imports = [
    ../../../modules/home-manager/go.nix
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

    # Nix
    niv # Nix sources manager
    nixpkgs-fmt

    # https://github.com/microsoft/vscode-remote-release/issues/648#issuecomment-503148523
    nodejs-18_x

    (python3.withPackages
      # install pip because its not included with the python3 nixpkg by
      # default
      (ps: with ps; [ pip pylint autopep8 ]))

    # Insert meme here
    kubectl
    kustomize
    kubectx
  ] ++ (lib.optionals pkgs.stdenv.isDarwin [
    # Manage macOS CoreFoundation libraries with Nix
    pkgs.darwin.CF
  ]);
}
