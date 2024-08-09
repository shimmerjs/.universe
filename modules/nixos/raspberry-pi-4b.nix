{ pkgs, lib, inputs, modulesPath, ... }:
{

  imports = [
    ./default.nix
    inputs.raspberry-pi-nix.nixosModules.raspberry-pi
    (modulesPath + "/installer/scan/not-detected.nix")
  ];

  # Correct value retrieved from https://github.com/nix-community/raspberry-pi-nix
  raspberry-pi-nix.board = "bcm2711";

  # Hardware configuration originally generated on a RPi 4b and inlined here.
  boot.initrd.availableKernelModules = [ "xhci_pci" ];
  boot.initrd.kernelModules = [ ];
  boot.kernelModules = [ ];
  boot.extraModulePackages = [ ];

  swapDevices = [ ];
  powerManagement.cpuFreqGovernor = lib.mkDefault "ondemand";

  # Taken from https://nixos.wiki/wiki/NixOS_on_ARM#Installation
  # NixOS wants to enable GRUB by default
  boot.loader.grub.enable = false;
  # Enables the generation of /boot/extlinux/extlinux.conf
  boot.loader.generic-extlinux-compatible.enable = true;
  boot.kernelPackages = pkgs.linuxPackages_latest;

  networking.interfaces.eth0.useDHCP = true;
}
