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
    casks = [
      "docker" # Tool for creation of human suffering
    ];
  };
}
