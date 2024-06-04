{ pkgs, ... }:
{
  programs.git = {
    extraConfig = {
      credential = {
        helper = "osxkeychain";
      };
    };
  };

  home.packages = with pkgs; [
    # Manage macOS CoreFoundation libraries with Nix
    darwin.CF

    rectangle # Simple window management.
  ];
}
