# Default configuration for Nix itself that is used for most hosts.
{ ... }:
{
  nix.settings.experimental-features = [ "nix-command" "flakes" ];

  # Trade disk space for cached builds
  nix.extraOptions = ''
    keep-outputs = true
    keep-derivations = true
  '';

  # Get disk space back, UK style, apparently
  nix.settings.auto-optimise-store = true;

  nix.gc = {
    automatic = true;
    interval = {
      Hour = 3;
      Minute = 0;
      Weekday = 3;
    };
  };

  nixpkgs.config = {
    allowUnfree = true; # SORRY
  };
}
