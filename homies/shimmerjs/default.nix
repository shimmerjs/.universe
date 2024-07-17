# Entrypoint for configuration that makes up the particular homie
# shimmerjs.
#
# It provides home-manager, nix-darwin, and nixos configuration in the 
# structure expected by lib/mksystem.nix that should be applied for this
# homie.
{
  home = import ./home;
  darwin = import ./darwin.nix;

  # TODO: nixos
  # nixos = nixcos.systemConfig;
}
