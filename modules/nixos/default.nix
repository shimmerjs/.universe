{ pkgs, user, hostname, ... }:
{
  imports = [
    ../nix.nix
    ../universe.nix

    ./ssh.nix
    ./sudo.nix
    ./tailscale.nix
  ];

  system.stateVersion = "22.05"; # Did you read the comment?
  time.timeZone = "America/New_York";

  # NixOS-specific nix.gc configuration
  nix.gc.dates = "weekly";

  networking.hostName = hostname;

  programs.zsh.enable = true;

  users.mutableUsers = false;
  users.users.${user} = {
    isNormalUser = true;
    extraGroups = [
      "root"
      "wheel"
      "networkmanager"
    ];
    home = "/home/${user}";
    shell = pkgs.zsh;
  };

  # Always ensure git is installed so we can pull the config repo.
  environment.systemPackages = with pkgs; [
    git
  ];
}
