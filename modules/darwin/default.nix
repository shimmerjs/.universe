{
  imports = [
    ../nix
  ];

  # Used for backwards compatibility, please read the changelog before changing.
  # $ darwin-rebuild changelog
  system.stateVersion = 4;

  nix = {
    # We install Nix using a separate installer for macOS, this setting tells 
    # nix-darwin to just use whatever is running.
    # TODO: can't use daemon because nix-darwin says it requires build users...
    # useDaemon = true;
    settings = {
      trusted-users = [ "root" "shimmerjs" ];
      allowed-users = [ "shimmerjs" ];
    };
  };

  programs.zsh.enable = true;

  users.users.shimmerjs = {
    name = "shimmerjs";
    # Explicitly set up user home directory to workaround nix-darwin issue:
    # https://github.com/LnL7/nix-darwin/issues/423
    home = "/Users/shimmerjs";
  };
}
