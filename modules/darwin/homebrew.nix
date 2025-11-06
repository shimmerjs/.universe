# Base homebrew config with sane defaults.
# TODO: make into module
{
  config,
  lib,
  user,
  ...
}:
{
  homebrew = {
    enable = true;

    onActivation.cleanup = "zap"; # Clean up removed apps

    caskArgs = {
      appdir = "~/Applications"; # Use non-global directory
    };

    user = user;

    global = {
      brewfile = true;
      autoUpdate = false;
    };
  };

  # Add homebrew binaries to PATH
  # TODO: assumes darwin-aarch64
  environment.systemPath = [ "/opt/homebrew/bin" ];
}
