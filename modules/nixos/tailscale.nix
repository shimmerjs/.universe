{
  # https://tailscale.com/kb/1063/install-nixos/
  services.tailscale.enable = true;
  networking.search = [ "tailafcef.ts.net" ];
  networking.nameservers = [
    "100.100.100.100"
    "8.8.8.8"
    "1.1.1.1"
  ];
}
