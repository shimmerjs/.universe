# Function that helps keep root flake.nix more declarative and abstracts away
# common machinery for host definitions, such as wiring up various required 
# modules.
#
# It also passes on additional special arguments that can be accessed by 
# configuration modules for better parameterization.
{ inputs }:

hostname:
let
  nixpkgs = inputs.nixpkgs;
  lib = nixpkgs.lib;
  attrByPath = lib.attrsets.attrByPath;

  # Assume config file path based on the system name provided. 
  # `hosts/$HOST.nix` and `hosts/$HOST/default.nix` are supported.
  hostFile =
    if builtins.pathExists ../hosts/${hostname}.nix
    then ../hosts/${hostname}.nix
    else ../hosts/${hostname}/default.nix;
  hostConfig = import hostFile;

  currentSystem = hostConfig.system;
  # TODO: allow homie to define user if present
  user = hostConfig.user;

  # Infer OS from system string.
  isDarwin = lib.strings.hasSuffix "darwin" currentSystem;

  # Get homies OS-specific configuration.
  homieOSConfig =
    if isDarwin
    then attrByPath [ "homie" "darwin" ] { } hostConfig
    else attrByPath [ "homie" "nixos" ] { } hostConfig;

  # We hook up home-manager if the host or the homie has defined a home function.
  loadHomeManager = hostConfig ? "home"
    || lib.hasAttrByPath [ "homie" "home" ] hostConfig
    || homieOSConfig ? "home";

  # Resolve functions for defining system and home-manager based on OS.
  systemFn =
    if isDarwin
    then inputs.darwin.lib.darwinSystem
    else nixpkgs.lib.nixosSystem;
  home-manager =
    if isDarwin
    then inputs.home-manager.darwinModules
    else inputs.home-manager.nixosModules;
in
systemFn rec {
  system = currentSystem;
  # Expose some extra args to our system config modules
  # Inspired by: https://github.com/mitchellh/nixos-config/blob/992fd3bc0984cd306e307fd59b22a37af77fca25/lib/mksystem.nix#L51-L58
  #
  # We use specialArgs instead of the config._module.args approach because using
  # config._module.args to populate module imports causes infinite recursion.
  # https://daiderd.com/nix-darwin/manual/index.html#opt-_module.args
  # 
  # This allows modules listed here to add these parameters, e.g.
  # { pkgs, config, lib, user, inputs, ...}: { ... }
  specialArgs = { inherit currentSystem hostname user inputs; };

  modules = [
    # Load host system configuration if present.
    hostConfig.systemConfig or { }
    # Load homie system configuration if present.
    homieOSConfig.systemConfig or { }
  ] ++ (lib.optionals isDarwin [
    # Ensure that apps installed via nix-darwin show up in Spotlight and the
    # Applications folder.
    inputs.mac-app-util.darwinModules.default
  ]) ++ (lib.optionals (hostConfig ? "diskConfig") [
    inputs.disko.nixosModules.disko
    hostConfig.diskConfig
  ]) ++ (lib.optionals loadHomeManager [
    home-manager.home-manager
    {
      home-manager.useGlobalPkgs = true;
      home-manager.useUserPackages = true;
      # Propagate the same set of specialArgs to home-manager configuration.
      home-manager.extraSpecialArgs = specialArgs;
      home-manager.users.${user}.imports = [
        hostConfig.home or { }
        hostConfig.homie.home or { }
        homieOSConfig.home or { }
      ];
      # Ensure that apps installed via home-manager show up in Spotlight and the
      # Applications folder as well.
      home-manager.sharedModules = (lib.optionals isDarwin
        [ inputs.mac-app-util.homeManagerModules.default ]
      );
    }
  ]);
}
