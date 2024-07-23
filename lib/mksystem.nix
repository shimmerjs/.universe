# Function that helps keep root flake.nix more declarative. Handles wiring up
# various Nix configurations for a specific host.
{ nixpkgs, inputs }:

hostname:
let
  lib = nixpkgs.lib;
  attrByPath = lib.attrsets.attrByPath;

  # Assume config file path based on the system name provided. 
  # `hosts/$HOST.nix` and `hosts/$HOST/default.nix` are supported.
  hostFile =
    if builtins.pathExists ../hosts/${hostname}.nix
    then ../hosts/${hostname}.nix
    else ../hosts/${hostname}/default.nix;
  hostConfig = import hostFile;

  system = hostConfig.system;
  # TODO: allow homie to define user if present
  user = hostConfig.user;

  # Define extra module arguments that are passed to system configurations and
  # home-manager configurations.
  moduleArgs = { inherit system hostname user inputs; };

  # Infer OS from system string.
  isDarwin = lib.strings.hasSuffix "darwin" system;

  # Get homies OS-specific configuration.
  # TODO: this probably doesnt gracefully handle partial objects
  homieOSConfig =
    if isDarwin
    then attrByPath [ "homie" "darwin" ] { } hostConfig
    else attrByPath [ "homie" "nixos" ] { } hostConfig;

  # We hook up home-manager if the host or the homie has defined a home function.
  loadHomeManager = hostConfig ? "home"
    || hostConfig.homie ? "home"
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
  inherit system;

  modules = [
    # Load host system configuration if present.
    hostConfig.systemConfig or { }
    # Load homie system configuration if present.
    homieOSConfig.systemConfig or { }

    # Ensure that apps installed via nix-darwin show up in Spotlight and the
    # Applications folder.
    (if isDarwin then inputs.mac-app-util.darwinModules.default else { })

    # Expose some extra args to our modules
    # Inspired by: https://github.com/mitchellh/nixos-config/blob/992fd3bc0984cd306e307fd59b22a37af77fca25/lib/mksystem.nix#L51-L58
    # This allows modules listed here to add these parameters, e.g.
    # { pkgs, config, lib, username, ...}: { ... }
    {
      config._module.args = moduleArgs;
    }
  ] ++ (lib.optionals loadHomeManager [
    home-manager.home-manager
    {
      home-manager.useGlobalPkgs = true;
      home-manager.useUserPackages = true;
      home-manager.extraSpecialArgs = moduleArgs;
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
