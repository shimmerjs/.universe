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
  # Setting networking.search / networking.dns without setting this value 
  # causes nix-darwin to vomit with an error that I can't find any additional
  # information about on the internet. Remaining values should be added by
  # importing hosts (`networksetup -listallnetworkservices`)
  networking.knownNetworkServices = [
    "Tailscale Tunnel"
  ];
}
