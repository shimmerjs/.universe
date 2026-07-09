{
  description = "Host and userland definitions for homies.";

  inputs = {
    # nixos-unstable, not master: master ships commits the binary cache hasn't
    # built yet, forcing from-source builds of heavy packages (e.g.
    # telegram-desktop's Qt/shader stack). nixos-unstable only advances once
    # Hydra has populated the cache. Tradeoff: a few days behind master; the one
    # package that matters to keep fresh (claude-code) is overridden from the
    # nixpkgs-claude input below, via the overlay in modules/darwin/default.nix.
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";

    # qemu on nixpkgs master (11.0.0) hits an HVF vCPU-init assertion on Apple
    # Silicon (target/arm/hvf/sysreg.c.inc: HV_SYS_REG_SMCR_EL1), so HVF refuses
    # to start a guest. Pin qemu to 10.2.2 -- the newest release before that
    # regression, verified HVF-working on aarch64-darwin. Consumed by the qemu
    # overlay in modules/darwin/default.nix. Drop once nixpkgs ships a fixed
    # qemu (a post-11.0.0 release).
    nixpkgs-qemu.url = "github:nixos/nixpkgs/4df1b885d76a54e1aa1a318f8d16fd6005b6401f";

    # Fresh claude-code only. The main nixpkgs tracks nixos-unstable (cache-warm
    # but a few days behind), which lags claude-code releases. This input tracks
    # master purely so the overlay in modules/darwin/default.nix can pull a
    # current claude-code; only that one package is consumed, so master's churn
    # and cache gaps don't matter. Bump with `nix flake update nixpkgs-claude`.
    nixpkgs-claude.url = "github:nixos/nixpkgs/master";

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
    # Git worktree manager; upstream flake because nixpkgs lags its
    # weekly release cadence
    worktrunk = {
      url = "github:max-sixty/worktrunk";
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

      checks = mkChecks self.darwinConfigurations self.nixosConfigurations;
    };
}
