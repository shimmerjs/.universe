{
  description = "Host and userland definitions for homies.";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/master";

    home-manager = {
      url = "github:nix-community/home-manager/master";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    darwin = {
      url = "github:LnL7/nix-darwin";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    # Manage disk configuration with Nix
    disko = {
      url = "github:nix-community/disko";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    # Secret management
    agenix = {
      url = "github:ryantm/agenix";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.darwin.follows = "darwin";
    };

    raspberry-pi-nix.url = "github:nix-community/raspberry-pi-nix";

    # Helps address issue where Nix-intalled apps don't show up in Spotlight
    # mac-app-util = {
    #  url = "github:hraban/mac-app-util";
    #  inputs.nixpkgs.follows = "nixpkgs";
    # };

    # Non-flake inputs
    powerlevel10k = {
      url = "github:romkatv/powerlevel10k";
      flake = false;
    };
  };

  outputs = inputs@{ self, nixpkgs, home-manager, darwin, disko, agenix, ... }:
    let
      mkSystem = import ./lib/mksystem.nix { inherit inputs; };
    in
    {
      darwinConfigurations = {
        nostromo = mkSystem "nostromo";
        aw-chainguard = mkSystem "aw-chainguard";
        mother = mkSystem "mother";
        kraken = mkSystem "kraken";
      };
      nixosConfigurations = {
        herq = mkSystem "herq";
        # Only used to create installable media for new hosts.
        freshmeat = mkSystem "freshmeat";
        expat = mkSystem "expat";
        slugger = mkSystem "slugger";
        snake = mkSystem "snake";
      };
    };
}
