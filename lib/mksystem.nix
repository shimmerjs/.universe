# Function that helps keep root flake.nix more declarative. Handles wiring up
# various Nix configurations for a specific host.
{ nixpkgs, inputs }:

name: { system
      , user
      , darwin ? false
      , homeMgr ? false
      , modules ? [ ]
      }:
let
  # Assume config files based on the system name provided.
  osConfig = ../hosts/${name}/configuration.nix;
  hmConfig = ../hosts/${name}/home.nix;

  # Use correct function for defining system and integrating home-manager based
  # on OS.
  systemFn = if darwin then inputs.darwin.lib.darwinSystem else nixpkgs.lib.nixosSystem;
  home-manager = if darwin then inputs.home-manager.darwinModules else inputs.home-manager.nixosModules;
in
systemFn rec {
  inherit system;

  modules = [
    osConfig
    # Utility that ensures apps installed via nix show up in Spotlight and 
    # the Applications folder
    (if darwin then inputs.mac-app-util.darwinModules.default else { })
  ] ++ (if homeMgr then # TODO: figure out how to make this conditional inline
    [
      home-manager.home-manager
      {
        home-manager.useGlobalPkgs = true;
        home-manager.useUserPackages = true;
        home-manager.users.${user} = import hmConfig { inputs = inputs; };
        home-manager.sharedModules = (if darwin then [ inputs.mac-app-util.homeManagerModules.default ] else [ ]);
      }
    ] else [ ]);
}
