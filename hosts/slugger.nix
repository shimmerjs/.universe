{
  system = "aarch64-linux";
  user = "k3s";

  systemConfig = { user, inputs, ... }: {
    imports = [
      ../modules/nixos/raspberry-pi-4b.nix
      inputs.agenix.nixosModules.default
    ];

    users.users.${user}.openssh.authorizedKeys = {
      keyFiles = [
        ../../homies/shimmerjs/shimmerjs.pub
      ];
    };
  };

  diskConfig = import ../../modules/nixos/disko/simple-gpt-lvm.nix {
    disk = "/dev/mmcblk1";
  };
}
