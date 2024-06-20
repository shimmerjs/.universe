{
  description = "Host and userland definitions for homies.";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixpkgs-unstable";

    home-manager = {
      url = "github:nix-community/home-manager/master";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    darwin = {
      url = "github:LnL7/nix-darwin";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    # Helps address issue where Nix-intalled apps don't show up in Spotlight
    mac-app-util.url = "github:hraban/mac-app-util";
  };

  outputs = inputs@{ self, nixpkgs, home-manager, darwin, ... }:
    let
      mkSystem = import ./lib/mksystem.nix { inherit nixpkgs inputs; };
    in
    {
      darwinConfigurations = {
        nostromo = mkSystem "nostromo" {
          system = "aarch64-darwin";
          user = "shimmerjs";
          darwin = true;
          homeMgr = true;
        };
        aw-chainguard = mkSystem "aw-chainguard" {
          system = "aarch64-darwin";
          user = "shimmerjs";
          darwin = true;
          homeMgr = true;
        };
        # TODO: personal mac mini
      };
      # TODO: nixosConfigurations:
      # - herq
      # - rpi k3s nodes
      # - old mac mini running nixos
      nixosConfigurations = {
        # Only used to create installable media for new hosts.
        # (Hardcoded because flakes.)
        freshmeat = mkSystem "freshmeat" {
          # TODO(?): default value for mkSystem?
          system = "x86_64-linux";
          # TODO: not actually used because no home-manager. factor out?
          user = "shimmerjs";
        };
      };
    };
}
