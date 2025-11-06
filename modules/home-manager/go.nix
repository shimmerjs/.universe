{ pkgs, config, ... }:
{
  programs.go = {
    package = pkgs.go_1_24;
    enable = true;
    env = {
      GOBIN = "${config.home.homeDirectory}/dev/go/bin";
      GOPATH = "${config.home.homeDirectory}/dev/go";
    };
    telemetry.mode = "off";
  };

  # Ensure binaries built with Go end up on PATH
  home.sessionPath = [
    "$(go env GOBIN)"
  ];

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
