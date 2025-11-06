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
      services = {
        sudo_local = {
          touchIdAuth = true;
        };
      };
    };
  };

  # Automatically apply macOS preference changes without requiring login/logout
  system.activationScripts.postActivation.text = ''
    # Following line should allow us to avoid a logout/login cycle
    # TODO: Use username parameter, dont poison root darwin module
    sudo -u ${user} /System/Library/PrivateFrameworks/SystemAdministration.framework/Resources/activateSettings -u
  '';
}
