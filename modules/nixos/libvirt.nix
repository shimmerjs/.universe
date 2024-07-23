{ pkgs, user, ... }:
{
  security.polkit.enable = true;
  virtualisation.libvirtd.enable = true;

  environment.systemPackages = with pkgs; [
    virt-manager
  ];

  users.users.${user}.extraGroups = [ "libvirtd" ];
}
