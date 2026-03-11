# Generates flake `checks` for all hosts that have nvim configured via nixCats.
# Automatically discovers eligible hosts from darwinConfigurations and
# nixosConfigurations — no hardcoded host list required.
{ inputs }:

let
  nixpkgs = inputs.nixpkgs;
  lib = nixpkgs.lib;

  # Resolve a host config file the same way mksystem.nix does.
  importHost = hostname:
    if builtins.pathExists ../hosts/${hostname}.nix
    then import ../hosts/${hostname}.nix
    else import ../hosts/${hostname}/default.nix;

  # Try to extract the nixCats nvim package from a system configuration.
  # Returns null if the host doesn't have nixCats/nvim configured.
  getNvim = hostname: config: let
    user = (importHost hostname).user;
    hmUserCfg = config.home-manager.users.${user} or {};
    nixCatsPkgs = hmUserCfg.nixCats.out.packages or {};
  in nixCatsPkgs.nvim or null;

  # Build an nvim startup check for a host. Returns null if host has no nvim.
  mkNvimCheck = hostname: config: let
    nvim = getNvim hostname config;
    system = (importHost hostname).system;
    pkgs = nixpkgs.legacyPackages.${system};
  in if nvim != null
    then { inherit system; name = "nvim-${hostname}"; value = import ./nvim-check.nix { inherit pkgs nvim; }; }
    else null;

  # Collect checks from a set of system configurations (darwin or nixos).
  collectChecks = configs:
    lib.pipe (lib.mapAttrsToList (name: sys: mkNvimCheck name sys.config) configs) [
      (builtins.filter (x: x != null))
    ];

in

darwinConfigurations: nixosConfigurations:
let
  allChecks = (collectChecks darwinConfigurations) ++ (collectChecks nixosConfigurations);
  systems = lib.unique (map (c: c.system) allChecks);
in
  lib.genAttrs systems (sys:
    builtins.listToAttrs
      (map (c: { inherit (c) name value; })
        (builtins.filter (c: c.system == sys) allChecks)))
