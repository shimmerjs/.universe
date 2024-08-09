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

  powerManagement.cpuFreqGovernor = lib.mkDefault "ondemand";

  networking.interfaces.eth0.useDHCP = true;
}
