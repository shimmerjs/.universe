# Generates flake `checks` for all hosts, discovered automatically from
# darwinConfigurations and nixosConfigurations — no hardcoded host list.
#   - nvim-<host>:            nvim startup check for hosts with nixCats/nvim.
#   - clod-workflows-<host>:  JS syntax + agentType-wiring lint for hosts that
#                             have a clod/workflows directory.
#   - clod-statusline-<host>: bash syntax + session-title layout smoke test for
#                             hosts that have a clod/statusline.sh.
#   - clod-workflow-tests-<host>: fixture unit tests (testdata/*.json) for the
#                             workflow parser and pure helpers, under plain node.
#   - khudson-bininstall-<host>: activation install-script legs (verify/tamper/
#                             fast-path) for hosts with khudson enabled.
#   - khudson-posture-<host>: RC + state-root posture pins on the rendered
#                             khudson artifacts, same gate.
#   - krib-sheets-<host>:     CUE-export drift guard for the committed
#                             pkgs/krib/sheets JSON artifacts.
#   - clod-plugins-<host>:    claude-code plugin-estate integrity on the
#                             rendered ~/.claude/skills (manifests seated,
#                             pointers resolve, no dangling symlinks).
#   - zshrc-<host>:           zsh rc posture pins (single compinit, cached
#                             -C, instant-prompt-first, no omz residue) +
#                             isolated runtime init smoke.
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

  # Run the workflow fixture unit tests (testdata/*.json against the extracted
  # parser/helper functions) under plain node. Deliberately does not touch
  # config (no wiredAgents needed). Returns null if the host has no
  # clod/workflows/testdata directory.
  mkWorkflowTestsCheck = hostname: config: let
    host = importHost hostname;
    system = host.system;
    pkgs = nixpkgs.legacyPackages.${system};
    workflowsDir = ../hosts/${hostname}/clod/workflows;
  in if builtins.pathExists (workflowsDir + "/testdata")
    then { inherit system; name = "clod-workflow-tests-${hostname}";
           value = import ./workflow-tests-check.nix { inherit pkgs workflowsDir; }; }
    else null;

  # Build the host's workflow cheatsheet (cheatsheet.nix -> cheatsheet-gen.mjs)
  # and assert one entry per aw-*.js, each with flags. Returns null if the host
  # has no clod/workflows/cheatsheet.nix.
  mkCheatsheetCheck = hostname: config: let
    host = importHost hostname;
    system = host.system;
    pkgs = nixpkgs.legacyPackages.${system};
    workflowsDir = ../hosts/${hostname}/clod/workflows;
    cheatsheetNix = workflowsDir + "/cheatsheet.nix";
    expected = builtins.length (builtins.attrNames (lib.filterAttrs
      (n: t: t == "regular" && lib.hasPrefix "aw-" n && lib.hasSuffix ".js" n)
      (builtins.readDir workflowsDir)));
  in if builtins.pathExists cheatsheetNix
    then { inherit system; name = "clod-cheatsheet-${hostname}";
           value = import ./cheatsheet-check.nix {
             inherit pkgs;
             cheatsheet = import cheatsheetNix { inherit pkgs; };
             expected = toString expected;
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

  # Behavioral tests for the clod hook estate: go-build-sweep narrowing,
  # gocheck's pinned-toolchain gate (fail-closed + drain semantics), the
  # go-fmt syntax gate + queue, and the aw-scriptpath gate. Returns null if
  # the host has no clod/hooks directory.
  mkHooksCheck = hostname: config: let
    host = importHost hostname;
    system = host.system;
    pkgs = nixpkgs.legacyPackages.${system};
    hooksDir = ../hosts/${hostname}/clod/hooks;
    hooks = import hooksDir { inherit pkgs; };
  in if builtins.pathExists hooksDir
    then { inherit system; name = "clod-hooks-${hostname}";
           value = import ./hooks-check.nix { inherit pkgs hooks; }; }
    else null;

  # khudson checks live beside the module (homies/shimmerjs/home/khudson/nix/)
  # and are gated on the module being enabled for the host's user -- no other
  # builder discovers khudson, so this is the one lib/ shim. Both import from
  # the module dir so the checked script/artifacts are the deployed ones.
  mkKhudsonInstallCheck = hostname: config: let
    host = importHost hostname;
    system = host.system;
    pkgs = nixpkgs.legacyPackages.${system};
    hmUserCfg = config.home-manager.users.${host.user} or {};
  in if hmUserCfg.universe.home.khudson.enable or false
    then { inherit system; name = "khudson-bininstall-${hostname}";
           value = import ../homies/shimmerjs/home/khudson/nix/install-check.nix { inherit pkgs; }; }
    else null;

  mkKhudsonPostureCheck = hostname: config: let
    host = importHost hostname;
    system = host.system;
    pkgs = nixpkgs.legacyPackages.${system};
    hmUserCfg = config.home-manager.users.${host.user} or {};
  in if hmUserCfg.universe.home.khudson.enable or false
    then { inherit system; name = "khudson-posture-${hostname}";
           value = import ../homies/shimmerjs/home/khudson/nix/posture-check.nix { inherit pkgs; hmCfg = hmUserCfg; }; }
    else null;

  # CUE-export drift guard for pkgs/krib/sheets: the committed JSON artifacts
  # (go:embed'd into krib) must equal `cue export` of their .cue sources.
  # Host-independent content, discovered per host like every other builder.
  mkKribSheetsCheck = hostname: config: let
    host = importHost hostname;
    system = host.system;
    pkgs = nixpkgs.legacyPackages.${system};
  in if builtins.pathExists ../pkgs/krib/sheets
    then { inherit system; name = "krib-sheets-${hostname}";
           value = import ./krib-sheets-check.nix { inherit pkgs; }; }
    else null;

  # Rendered claude-code plugin estate must load under CC's discovery rules
  # (manifests seated at .claude-plugin/plugin.json, pointers resolving, no
  # dangling symlinks, no store-hash entry names): the 2026-07 home-manager
  # personal-plugin rewire broke this class silently. Gated on the host's
  # user enabling programs.claude-code.
  mkClaudePluginsCheck = hostname: config: let
    host = importHost hostname;
    system = host.system;
    pkgs = nixpkgs.legacyPackages.${system};
    hmUserCfg = config.home-manager.users.${host.user} or {};
  in if hmUserCfg.programs.claude-code.enable or false
    then { inherit system; name = "clod-plugins-${hostname}";
           value = import ./claude-plugins-check.nix {
             inherit pkgs;
             homeFiles = hmUserCfg.home-files;
           }; }
    else null;

  # zsh rc posture pins + isolated init smoke on the rendered rc files.
  # shimmerjs-gated rather than zsh-gated: building it needs the user's
  # rendered .zshrc, and forcing scott's home config trips kraken's
  # pre-existing rectangle eval failure.
  mkZshrcCheck = hostname: config: let
    host = importHost hostname;
    system = host.system;
    pkgs = nixpkgs.legacyPackages.${system};
    hmUserCfg = config.home-manager.users.${host.user} or {};
  in if host.user == "shimmerjs" && (hmUserCfg.programs.zsh.enable or false)
    then { inherit system; name = "zshrc-${hostname}";
           value = import ./zshrc-check.nix {
             inherit pkgs;
             # from the rendered home tree, not home.file internals: the
             # zsh module's file attr key changes with dotDir handling
             zshrc = "${hmUserCfg.home-files}/.zshrc";
             etcZshrc = config.environment.etc."zshrc".text;
           }; }
    else null;

  checkBuilders = [ mkNvimCheck mkWorkflowCheck mkCheatsheetCheck mkStatuslineCheck mkHooksCheck mkWorkflowTestsCheck mkKhudsonInstallCheck mkKhudsonPostureCheck mkKribSheetsCheck mkClaudePluginsCheck mkZshrcCheck ];

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
