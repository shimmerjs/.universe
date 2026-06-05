# Default configuration for home-manager, intended to be used by any hosts that
# use home-manager, regardless of OS.
#
# Should not assume graphical environment
{ pkgs, lib, ... }:
{
  programs.home-manager.enable = true;
  home.stateVersion = "21.03"; # Version set by home-manager when first installed.

  home.sessionPath = [
    "$HOME/.local/bin"
  ];

  # Ensure that installed apps are indexed by Spotlight on macOS
  targets.darwin = lib.mkIf pkgs.stdenv.hostPlatform.isDarwin {
    copyApps.enable = true;
    linkApps.enable = false;
  };

  programs.ssh = {
    enable = true;
    enableDefaultConfig = false;
    settings."*" = {
      ForwardAgent = false;
      AddKeysToAgent = "no";
      Compression = false;
      ServerAliveInterval = 0;
      ServerAliveCountMax = 3;
      HashKnownHosts = false;
      UserKnownHostsFile = "~/.ssh/known_hosts";
      ControlMaster = "no";
      ControlPath = "~/.ssh/master-%r@%n:%p";
      ControlPersist = "no";
    };
  };

  # Packages that should be everywhere, basically.
  home.packages = with pkgs; [
    curl
    fzf
    bat
    htop
    jq
    coreutils
    ack
    tree
  ];
}
