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

  # TODO: default browser
}
