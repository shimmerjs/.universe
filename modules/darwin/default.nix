# Default macOS system configuration applied to all macOS hosts. Additional 
# modules are then layered on top or defined as part of the host definition.
{ user, hostname, ... }:
{
  imports = [
    ../nix.nix
    ../universe.nix
  ];

  # Used for backwards compatibility, please read the changelog before changing.
  # $ darwin-rebuild changelog
  system.stateVersion = 4;

  nix = {
    # We install Nix using a separate installer for macOS, this setting tells 
    # nix-darwin to just use whatever is running. It is also required for
    # multi-user builds, which is the default for all newer macOS nix 
    # installations.
    useDaemon = true;
    # GC configuration that is specific to nix-darwin
    gc = {
      interval = {
        Hour = 3;
        Minute = 0;
        Weekday = 3;
      };
    };
  };

  networking = {
    hostName = hostname;
    computerName = hostname;
  };

  programs.zsh.enable = true;

  users.users.${user} = {
    name = user;
    # Explicitly set up user home directory to workaround nix-darwin issue:
    # https://github.com/LnL7/nix-darwin/issues/423
    home = "/Users/${user}";
  };

  security = {
    pam = {
      enableSudoTouchIdAuth = true;
    };
  };

  # Automatically apply macOS preference changes without requiring login/logout
  system.activationScripts.postUserActivation.text = ''
    # Following line should allow us to avoid a logout/login cycle
    /System/Library/PrivateFrameworks/SystemAdministration.framework/Resources/activateSettings -u
  '';
}
