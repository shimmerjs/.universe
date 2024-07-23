{
  system = "x86_64-linux";
  user = "shimmerjs";

  homie = import ../../homies/shimmerjs;

  systemConfig = { pkg, lib, config, user, ... }: {
    imports = [
      ../../modules/nixos
      ../../modules/nixos/libvirt.nix

      ./hardware.nix
    ];

    networking.useDHCP = false;
    networking.interfaces.eno1.useDHCP = true;

    boot.loader.systemd-boot.enable = true;
    boot.loader.efi.canTouchEfiVariables = true;

    boot.loader.grub.device = "/dev/disk/by-uuid/18AA-4CFE";
    boot.kernel.sysctl = {
      "fs.inotify.max_user_watches" = "1048576";
    };

    # Enable emulating aarch64 for building raspberry pi images
    boot.binfmt.emulatedSystems = [ "aarch64-linux" ];

    users.users.${user}.openssh.authorizedKeys = {
      keyFiles = [
        ../../homies/shimmerjs/shimmerjs.pub
      ];
    };
  };
}
