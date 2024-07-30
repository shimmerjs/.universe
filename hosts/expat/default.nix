{
  system = "x86_64-linux";
  user = "k3s";

  systemConfig = { pkg, lib, config, user, inputs, ... }: {
    imports = [
      ../../modules/nixos
      ../../modules/nixos/k3s-server.nix

      inputs.agenix.nixosModules.default

      ./hardware.nix
    ];

    boot.loader.systemd-boot.enable = true;
    boot.loader.efi.canTouchEfiVariables = true;

    age.secrets.k3s-server-token.file = ../../homies/shimmerjs/secrets/k3s-server-token.age;
    services.k3s.tokenFile = age.secrets.k3s-server-token.path;

    users.users.${user}.openssh.authorizedKeys = {
      keyFiles = [
        ../../homies/shimmerjs/shimmerjs.pub
      ];
    };
  };

  diskConfig = import ../../modules/nixos/disko/simple-gpt-lvm.nix {
    disk = "/dev/nvme0n1";
  };
}
