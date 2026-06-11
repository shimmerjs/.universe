{ pkgs, config, ... }:
{
  programs.go = {
    package = pkgs.go;
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

  home.packages = import ./go-tools.nix pkgs;
}
