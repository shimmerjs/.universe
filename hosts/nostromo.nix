# Personal macbook.
{
  system = "aarch64-darwin";
  user = "shimmerjs";
  homie = import ../homies/shimmerjs;

  systemConfig = { config, lib, pkgs, ... }: {
    homebrew = {
      casks = [
        "protonvpn"
        "balenaetcher"
      ];
    };
  };
}
