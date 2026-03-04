# Default configuration for home-manager, intended to be used by any hosts that
# use home-manager, regardless of OS.
#
# Should not assume graphical environment
{ pkgs, ... }:
{
  programs.home-manager.enable = true;
  home.stateVersion = "21.03"; # Version set by home-manager when first installed.

  home.sessionPath = [
    "$HOME/.local/bin"
  ];

  # Ensure that installed apps are indexed by Spotlight on macOS
  targets.darwin = {
    copyApps.enable = true;
    linkApps.enable = false;
  };

  programs.ssh = {
    enable = true;
    enableDefaultConfig = false;
    matchBlocks."*" = {
      forwardAgent = false;
      addKeysToAgent = "no";
      compression = false;
      serverAliveInterval = 0;
      serverAliveCountMax = 3;
      hashKnownHosts = false;
      userKnownHostsFile = "~/.ssh/known_hosts";
      controlMaster = "no";
      controlPath = "~/.ssh/master-%r@%n:%p";
      controlPersist = "no";
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
