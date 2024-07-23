# Default configuration for Nix itself that is used for most hosts.
{ user, ... }:
{
  nix = {
    settings = {
      # TODO: support additional users via options
      experimental-features = [ "nix-command" "flakes" ];
      trusted-users = [ "root" user ];
      allowed-users = [ user ];
      # Get disk space back, UK style, apparently
      auto-optimise-store = true;
    };
    gc = {
      automatic = true;
    };
    # Trade disk space for cached builds
    extraOptions = ''
      keep-outputs = true
      keep-derivations = true
    '';
  };

  nixpkgs.config = {
    allowUnfree = true; # SORRY
  };
}
