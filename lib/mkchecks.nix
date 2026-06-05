# Generates flake `checks` for all hosts, discovered automatically from
# darwinConfigurations and nixosConfigurations — no hardcoded host list.
#   - nvim-<host>:            nvim startup check for hosts with nixCats/nvim.
#   - clod-workflows-<host>:  JS syntax + agentType-wiring lint for hosts that
#                             have a clod/workflows directory.
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

  # agentType names that always resolve regardless of user config.
  builtinAgents = [ "claude" "Explore" "general-purpose" "Plan" "statusline-setup" "claude-code-guide" ];

  # Lint a host's clod workflows: JS syntax + every agentType resolves to a
  # built-in or an agent wired via programs.claude-code.agents. Returns null if
  # the host has no clod/workflows directory.
  mkWorkflowCheck = hostname: config: let
    host = importHost hostname;
    system = host.system;
    pkgs = nixpkgs.legacyPackages.${system};
    workflowsDir = ../hosts/${hostname}/clod/workflows;
    hmUserCfg = config.home-manager.users.${host.user} or {};
    wiredAgents = builtins.attrNames (hmUserCfg.programs.claude-code.agents or {});
  in if builtins.pathExists workflowsDir
    then { inherit system; name = "clod-workflows-${hostname}";
           value = import ./workflow-check.nix {
             inherit pkgs lib workflowsDir;
             validAgents = builtinAgents ++ wiredAgents;
           }; }
    else null;

  checkBuilders = [ mkNvimCheck mkWorkflowCheck ];

  # Collect checks from a set of system configurations (darwin or nixos).
  collectChecks = configs:
    builtins.filter (x: x != null)
      (lib.concatMap
        (builder: lib.mapAttrsToList (name: sys: builder name sys.config) configs)
        checkBuilders);

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
