# Personal macmini
{
  system = "aarch64-darwin";
  user = "shimmerjs";

  homie = import ../homies/shimmerjs;

  systemConfig = { user, ... }: {
    imports = [
      ../modules/darwin/tailscale.nix
    ];

    networking.knownNetworkServices = [
      "Ethernet"
      "Thunderbolt Bridge"
      "Wi-Fi"
    ];

    users.users.${user}.openssh.authorizedKeys = {
      keyFiles = [
        ../homies/shimmerjs/shimmerjs.pub
      ];
    };
  };
}
