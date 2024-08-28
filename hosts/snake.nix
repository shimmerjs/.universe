{
  system = "aarch64-linux";
  user = "k3s";

  systemConfig = { user, inputs, ... }: {
    imports = [
      ../modules/nixos/raspberry-pi-4b.nix
      inputs.agenix.nixosModules.default
    ];

    # Required for ZFS pools on NixOS.
    # TODO: automatically generate when needed
    networking.hostId = "3bb167cd";

    users.users.${user}.openssh.authorizedKeys = {
      keyFiles = [
        ../homies/shimmerjs/shimmerjs.pub
      ];
    };
  };

  diskConfig = {
    disko.devices = {
      disk = {
        a = {
          type = "disk";
          device = "/dev/sda";
          content = {
            type = "gpt";
            partitions = {
              zfs = {
                size = "100%";
                content = {
                  type = "zfs";
                  pool = "zroot";
                };
              };
            };
          };
        };
        b = {
          type = "disk";
          device = "/dev/sdb";
          content = {
            type = "gpt";
            partitions = {
              zfs = {
                size = "100%";
                content = {
                  type = "zfs";
                  pool = "zroot";
                };
              };
            };
          };
        };
      };
      zpool = {
        zroot = {
          type = "zpool";
          mode = "mirror";
          rootFsOptions = {
            compression = "zstd";
            "com.sun:auto-snapshot" = "false";
          };
          mountpoint = "/storage";
          postCreateHook = "zfs list -t snapshot -H -o name | grep -E '^zroot@blank$' || zfs snapshot zroot@blank";

          datasets = {
            zfs_fs = {
              type = "zfs_fs";
              mountpoint = "/zfs_fs";
              options."com.sun:auto-snapshot" = "true";
            };
            zfs_unmounted_fs = {
              type = "zfs_fs";
              options.mountpoint = "none";
            };
          };
        };
      };
    };
  };
}
