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

    raspberry-pi-nix.url = "github:nix-community/raspberry-pi-nix";

    # Manage disk configuration with Nix
    disko = {
      url = "github:nix-community/disko";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    # Packages
    agenix = {
      url = "github:ryantm/agenix";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.darwin.follows = "darwin";
    };
    spotatui = {
      url = "github:shimmerjs/spotatui";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    # Utility to switch tabs in kitty terminal using fzf
    kitty-tab-switcher = {
      url = "github:OsiPog/kitty-tab-switcher";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    # Automatically up-to-date vscode extensions for nix
    nix-vscode-extensions = {
      url = "github:nix-community/nix-vscode-extensions";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    # nvim frameworky thing
    nixCats.url = "github:BirdeeHub/nixCats-nvim";

    # nvim plugins not in nixpkgs (picked up by nixCats standardPluginOverlay)
    plugins-telescope-switch = {
      url = "github:sshelll/telescope-switch.nvim";
      flake = false;
    };
    plugins-adjacent-nvim = {
      url = "github:MaximilianLloyd/adjacent.nvim";
      flake = false;
    };
    plugins-telescope-recent-files = {
      url = "github:smartpde/telescope-recent-files";
      flake = false;
    };

    # Non-flake inputs
    powerlevel10k = {
      url = "github:romkatv/powerlevel10k";
      flake = false;
    };
  };

  outputs =
    inputs@{
      self,
      nixpkgs,
      home-manager,
      darwin,
      disko,
      agenix,
      ...
    }:
    let
      mkSystem = import ./lib/mksystem.nix { inherit inputs; };
      mkChecks = import ./lib/mkchecks.nix { inherit inputs; };
    in
    {
      darwinConfigurations = {
        nostromo = mkSystem "nostromo";
        aw-chainguard = mkSystem "aw-chainguard";
        mother = mkSystem "mother";
      };
      nixosConfigurations = {
        herq = mkSystem "herq";
        # Only used to create installable media for new hosts.
        freshmeat = mkSystem "freshmeat";
        expat = mkSystem "expat";
        slugger = mkSystem "slugger";
        snake = mkSystem "snake";
      };

      checks = mkChecks self.darwinConfigurations self.nixosConfigurations;
    };
}
