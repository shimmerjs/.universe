# Default configuration for Nix itself that is used for most hosts.
{ ... }:
{
  nix.settings.experimental-features = [ "nix-command" "flakes" ];

  nixpkgs.config = {
    allowUnfree = true; # SORRY
  };
}
