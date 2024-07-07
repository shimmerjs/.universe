# nix-darwin config for non-work machines
{ pkgs, ... }:
{
  homebrew = {
    casks = [
      "bitwarden" # Bitwarden desktop app
      "protonvpn"
    ];
  };
}
