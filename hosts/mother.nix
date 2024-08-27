# Personal macmini
{
  system = "aarch64-darwin";
  user = "shimmerjs";

  homie = import ../homies/shimmerjs;

  systemConfig = { pkgs, user, ... }: {
    imports = [
      ../modules/darwin/tailscale.nix
    ];

    environment.systemPackages = with pkgs; [
      monero-cli
    ];

    networking.knownNetworkServices = [
      "Ethernet"
      "Thunderbolt Bridge"
      "Wi-Fi"
      "ProtonVPN"
    ];

    users.users.${user}.openssh.authorizedKeys = {
      keyFiles = [
        ../homies/shimmerjs/shimmerjs.pub
      ];
    };
  };
}
