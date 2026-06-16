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

    onActivation.autoUpdate = true; # Update brew index during activation
    onActivation.cleanup = "zap"; # Clean up removed apps
    # Modern Homebrew (>=~5.x) refuses a non-interactive destructive --cleanup
    # without an explicit force flag. nix-darwin emits `brew bundle --cleanup --zap`
    # with no force, so append it here.
    onActivation.extraFlags = [ "--force-cleanup" ];

    # Homebrew >=6 refuses to load formulae from third-party taps unless trusted
    # via `brew trust` (state in ~/.homebrew/trust.json). The Brewfile here is
    # nix-generated, so skip the gate for the activation run only; hosts that use
    # third-party taps interactively should also declare trust.json via
    # home-manager. Deprecated upstream -- becomes a no-op when removed, at which
    # point the declared trust.json carries it.
    onActivation.extraEnv.HOMEBREW_NO_REQUIRE_TAP_TRUST = "1";

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
