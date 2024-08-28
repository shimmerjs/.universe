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
            zfs_legacy_fs = {
              type = "zfs_fs";
              options.mountpoint = "legacy";
              mountpoint = "/zfs_legacy_fs";
            };
            zfs_testvolume = {
              type = "zfs_volume";
              size = "10M";
              content = {
                type = "filesystem";
                format = "ext4";
                mountpoint = "/ext4onzfs";
              };
            };
          };
        };
      };
    };
  };
}
