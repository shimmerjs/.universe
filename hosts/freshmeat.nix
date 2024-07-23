# ISO image for bootstrapping new machines, structured to integrate with
# nix flakes.
{
  system = "x86_64-linux";
  user = "shimmerjs";

  systemConfig = import ../modules/nixos/iso.nix;
}

# TODO: deal with ssh/root.nix not existing anymore, should keep iso.nix pure
# {
#   users.extraUsers.root.openssh.authorizedKeys.keys = [
#     (builtins.readFile ./keys/booninite.keys)
#     (builtins.readFile ./keys/shimmerjs-key)
#   ];
# }
