{ pkgs, ... }:
{
  programs.go = {
    enable = true;
    goBin = "dev/go/bin";
    goPath = "dev/go";
  };

  # Go tools that are needed by editor tooling (gopls) or are just useful.
  home.packages = with pkgs; [
    gopls
    gopkgs
    godef
    golint
    gocode-gomod
    gotools
    golangci-lint
  ];
}
