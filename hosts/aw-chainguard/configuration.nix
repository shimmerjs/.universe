# Personal macbook.
{ config, lib, pkgs, ... }:
{
  imports = [
    ../../modules/darwin
  ];

  networking = {
    hostName = "aw-chainguard";
    computerName = "aw-chainguard";
  };

  homebrew = {
    taps = [
      "chainguard-dev/tap" # Needed for chainctl, not gitsign
    ];
    brews = [
      "gitsign"
      "chainctl"
    ];
    casks = [
      "docker" # Tool for creation of human suffering
    ];
  };
}
