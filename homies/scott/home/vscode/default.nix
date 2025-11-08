{ pkgs, ... }:
{
  programs.vscode = with pkgs; {
    enable = true;
    mutableExtensionsDir = false;
    profiles.default = {
      extensions = with vscode-extensions; [
        # Pull remote-ssh extension from nixpkgs to pick up special patches that
        # fix interactions with the remote development server the extension tries
        # to set up.
        ms-vscode-remote.remote-ssh
      ] ++ vscode-utils.extensionsFromVscodeMarketplace (import ./extensions.nix).extensions;
      userSettings = import ./user-settings.nix;
      keybindings = import ./keybindings.nix;
      enableExtensionUpdateCheck = false;
      enableUpdateCheck = false;
    };
  };

  programs.zsh = {
    sessionVariables = {
      EDITOR = "code --wait";
    };
  };
}
