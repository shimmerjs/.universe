let
  herq = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDnKDesViaz/KHV+/D5cIxxaz63PbY9qXfxoeq0sUYOG";
  expat = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIF0yuUM8L28LB4efcOxTA7VJAoiWJe5/1DdsVLnYd9eT";

  systems = [
    herq
    expat
  ];

  k3s = [
    expat
  ];

  shimmerjs = builtins.readFile homies/shimmerjs/shimmerjs.pub;
in
{
  "homies/shimmerjs/secrets/k3s-server-token.age".publicKeys = [
    shimmerjs
  ] ++ k3s;
}
