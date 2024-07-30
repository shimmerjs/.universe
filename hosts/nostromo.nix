# Personal macbook.
{
  system = "aarch64-darwin";
  user = "shimmerjs";
  homie = import ../homies/shimmerjs;

  systemConfig = { inputs, ... }: {
    imports = [
      ../modules/darwin/tailscale.nix
    ];

    # Nostromo only manages secrets with `agenix`, it doesn't need
    # the full module to reference secrets.
    environment.systemPackages = with inputs; [
      agenix.packages.aarch64-darwin.default
    ];

    networking.knownNetworkServices = [
      "USB 10/100/1000 LAN"
      "Thunderbolt Ethernet Slot 0"
      "Thunderbolt Bridge"
      "Wi-Fi"
      "iPhone USB"
      "ProtonVPN"
    ];

    homebrew = {
      casks = [
        "protonvpn"
        "balenaetcher"
      ];
    };
  };

  home = { ... }: {
    # TODO: paramterize WAN IP
    # TODO: generate this configuration from node information
    programs.ssh = {
      extraConfig = ''
        Host herqtail
          HostName herq

        Host herq
          HostName 192.168.1.226

        Host herqmo
          HostName 107.223.187.30
      '';
    };
  };
}
