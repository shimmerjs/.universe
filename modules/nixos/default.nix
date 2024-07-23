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

  programs.zsh.enable = true;

  users.mutableUsers = false;
  users.users.${user} = {
    isNormalUser = true;
    extraGroups = [
      "root"
      "wheel"
      "networkmanager"
    ];
    honme = "/home/${user}";
    shell = pkgs.zsh;
  };
}
