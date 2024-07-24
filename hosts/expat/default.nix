{
  system = "x86_64-linux";
  user = "shimmerjs";

  systemConfig = { pkg, lib, config, user, inputs, ... }: {
    imports = [
      ../../modules/nixos

      ./hardware.nix
    ];

    boot.loader.systemd-boot.enable = true;
    boot.loader.efi.canTouchEfiVariables = true;

    users.users.${user}.openssh.authorizedKeys = {
      keyFiles = [
        ../../homies/shimmerjs/shimmerjs.pub
      ];
    };
  };

  diskConfig = import ../../modules/nixos/disko/simple-gpt-lvm.nix {
    disk = "nvme0n1";
  };
}
