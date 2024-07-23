# A minimal definition used to build ISO images for new machines.
# Allows SSHing in for headless install with passwordless sudo.
{ config, pkgs, hostname, inputs, ... }:
{
  imports = [
    "${inputs.nixpkgs}/nixos/modules/installer/cd-dvd/installation-cd-minimal.nix"

    # Provide an initial copy of the NixOS channel so that the user
    # doesn't need to run "nix-channel --update" first.
    "${inputs.nixpkgs}/nixos/modules/installer/cd-dvd/channel.nix"

    # passwordless root on image
    ./nixos/security/sudo.nix
    # root ssh key setup for installation
    # ./nixos/ssh/root.nix
    # include tailscale config so that we can register the 
    # node with our mesh during bootstrapping
    # ./nixos/networking
  ];

  # Enable SSH in the boot process.
  systemd.services.sshd.wantedBy = pkgs.lib.mkForce [ "multi-user.target" ];

  # Trade build speed for file size.
  isoImage.squashfsCompression = "gzip -Xcompression-level 1";

  networking.hostName = hostname;
}
