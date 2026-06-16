# Default configuration for Nix itself that is used for most hosts.
{ user, ... }:
{
  nix = {
    settings = {
      # TODO: support additional users via options
      experimental-features = [
        "nix-command"
        "flakes"
      ];
      trusted-users = [
        "root"
        user
      ];
      allowed-users = [ user ];
    };
    # Get disk space back, UK style, apparently
    optimise.automatic = true;
    gc = {
      automatic = true;
      # Without this, gc only collects unreachable paths -- and every old
      # system generation keeps its whole closure reachable. 334 generations
      # deep that is hundreds of GB of pinned dead worlds.
      options = "--delete-older-than 30d";
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
