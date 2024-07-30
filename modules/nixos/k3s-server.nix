# Basic server configuration for k3s. Users should explicitly set
# services.k3s.tokenFile to make coordination with registered nodes easier.
{
  # https://github.com/NixOS/nixpkgs/blob/master/pkgs/applications/networking/cluster/k3s/docs/USAGE.md#single-node
  # Required for nodes to reach API server
  networking.firewall.allowedTCPPorts = [ 6443 ];
  # Required for inter-node communication over flannel
  networking.firewall.allowedUDPPorts = [ 8472 ];

  services.k3s = {
    enable = true;
    role = "server";
  };
}
