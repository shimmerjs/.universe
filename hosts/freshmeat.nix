# ISO image for bootstrapping new machines, structured to integrate with
# nix flakes.
{
  system = "x86_64-linux";
  user = "shimmerjs";

  systemConfig = import ../modules/nixos/iso.nix {
    rootSSHKeyFile = ../homies/shimmerjs/shimmerjs.pub;
  };
}
