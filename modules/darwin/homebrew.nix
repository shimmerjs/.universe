# Base homebrew config with sane defaults.
# TODO: make into module
{ config, lib, ... }:
{
  homebrew = {
    enable = true;

    onActivation.cleanup = "uninstall"; # Clean up removed apps

    caskArgs = {
      appdir = "~/Applications"; # Use non-global directory
    };
  };

  # Add homebrew binaries to PATH
  # TODO: assumes darwin-aarch64
  environment.systemPath = [ "/opt/homebrew/bin" ];
}
