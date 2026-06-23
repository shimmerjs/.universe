# Generates flake `checks` for all hosts, discovered automatically from
# darwinConfigurations and nixosConfigurations — no hardcoded host list.
#   - nvim-<host>:            nvim startup check for hosts with nixCats/nvim.
#   - clod-workflows-<host>:  JS syntax + agentType-wiring lint for hosts that
#                             have a clod/workflows directory.
#   - clod-statusline-<host>: bash syntax + session-title layout smoke test for
#                             hosts that have a clod/statusline.sh.
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

  # Smoke-test a host's clod statusline (bash syntax + session-title layout).
  # Returns null if the host has no clod/statusline.sh.
  mkStatuslineCheck = hostname: config: let
    host = importHost hostname;
    system = host.system;
    pkgs = nixpkgs.legacyPackages.${system};
    statuslineScript = ../hosts/${hostname}/clod/statusline.sh;
  in if builtins.pathExists statuslineScript
    then { inherit system; name = "clod-statusline-${hostname}";
           value = import ./statusline-check.nix { inherit pkgs statuslineScript; }; }
    else null;

  # Behavioral test for the go-build-sweep hook (narrow to this-build's binary,
  # leave unrelated/tracked binaries alone). Returns null if the host has no
  # clod/hooks/go-build-sweep.sh.
  mkHooksCheck = hostname: config: let
    host = importHost hostname;
    system = host.system;
    pkgs = nixpkgs.legacyPackages.${system};
    sweepScript = ../hosts/${hostname}/clod/hooks/go-build-sweep.sh;
    hooks = import ../hosts/${hostname}/clod/hooks { inherit pkgs; };
  in if builtins.pathExists sweepScript
    then { inherit system; name = "clod-hooks-${hostname}";
           value = import ./hooks-check.nix { inherit pkgs; goBuildSweep = hooks.goBuildSweep; }; }
    else null;

  checkBuilders = [ mkNvimCheck mkWorkflowCheck mkStatuslineCheck mkHooksCheck ];

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
