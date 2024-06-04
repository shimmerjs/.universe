# A minimal definition used to build ISO images for new machines.
# Allows SSHing in for headless install and passwordless sudo.
# 
# For the purposes of integrating with our flake.nix and using the already pinned
# inputs, it is treated like any other host definition.
{ config, pkgs, ... }:
{
  imports = [
    # Base ISO
    ./${pkgs}/nixos/modules/installer/cd-dvd/installation-cd-minimal.nix

    # Provide an initial copy of the NixOS channel so that the user
    # doesn't need to run "nix-channel --update" first.
    ./${pkgs}/nixos/modules/installer/cd-dvd/channel.nix

    # passwordless root on image
    ../../modules/nixos/security/sudo.nix
    # root ssh key setup for installation
    ../../modules/nixos/ssh/root.nix
    # include tailscale config so that we can register the 
    # node with our mesh during bootstrapping
    ../../modules/nixos/networking
  ];

  # Enable SSH in the boot process.
  systemd.services.sshd.wantedBy = pkgs.lib.mkForce [ "multi-user.target" ];

  networking.hostName = "freshmeat";
  # TODO: ?
  # networking.hostName = config._module.args[...];

  # Trade build speed for file size.
  isoImage.squashfsCompression = "gzip -Xcompression-level 1";
}
