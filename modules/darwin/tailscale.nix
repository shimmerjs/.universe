# This is partially duplicated from modules/nixos/tailscale.nix because the
# options slightly differ because nobody cares that the world is burning.
{
  services.tailscale.enable = true;
  networking.search = [ "tailafcef.ts.net" ];
  networking.dns = [
    "100.100.100.100"
    "8.8.8.8"
    "1.1.1.1"
  ];
}
