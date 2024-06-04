{ config, pkgs, ... }:
{
  imports = [
    ./${pkgs}/nixos/modules/installer/cd-dvd/installation-cd-minimal.nix>

    # Provide an initial copy of the NixOS channel so that the user
    # doesn't need to run "nix-channel --update" first.
    ./${pkgs}/nixos/modules/installer/cd-dvd/channel.nix>

    # passwordless root on image
    ../nixos/security/sudo.nix>
    # root ssh key setup for installation
    ../nixos/ssh/root.nix>
    # include tailscale config so that we can register the 
    # node with our mesh during bootstrapping
    ../nixos/networking>
  ];

  # Enable SSH in the boot process.
  systemd.services.sshd.wantedBy = pkgs.lib.mkForce [ "multi-user.target" ];

  networking.hostName = "fresh-iso";
}
