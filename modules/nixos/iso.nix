# A minimal definition used to build ISO images for new machines.
# Allows SSHing in for headless install with passwordless sudo.
# TODO: Add install scripts to this to simplify the various steps
{ rootSSHKeyFile }:
{ config, pkgs, hostname, modulesPath, ... }:
{
  imports = [
    "${modulesPath}/installer/cd-dvd/installation-cd-minimal.nix"

    # Provide an initial copy of the NixOS channel so that the user
    # doesn't need to run "nix-channel --update" first.
    "${modulesPath}/installer/cd-dvd/channel.nix"

    # Passwordless sudo on image
    ./sudo.nix
    # Configure Nix to enable flakes, etc
    ../nix.nix
  ];

  # Make sure we can git clone config repo on new host
  environment.systemPackages = with pkgs; [
    git
  ];

  # Enable SSH in the boot process.
  systemd.services.sshd.wantedBy = pkgs.lib.mkForce
    [ "multi-user.target" ];

  # Trade build speed for file size.
  isoImage.squashfsCompression = "gzip -Xcompression-level 1";

  networking.hostName = hostname;

  users.extraUsers.root.openssh.authorizedKeys.keyFiles = [
    rootSSHKeyFile
  ];
}
