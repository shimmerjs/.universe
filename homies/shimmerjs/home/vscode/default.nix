{ pkgs, inputs, ... }:
let
  # Load all extensions from nix-vscode package instead of nixpkgs, other than
  # remote-ssh, which has required special patches
  vscodeExts = inputs.nix-vscode-extensions.extensions.${pkgs.stdenv.hostPlatform.system};
  exts =
    with vscodeExts;
    with vscode-marketplace;
    [
      bazelbuild.vscode-bazel
      bierner.markdown-mermaid
      cuelangorg.vscode-cue
      fcrespo82.markdown-table-formatter
      golang.go
      terrastruct.d2
      sainnhe.everforest
      maattdd.gitless
      ms-python.python
      jnoortheen.nix-ide
    ];
in
{
  programs.vscode = with pkgs; {
    enable = true;
    mutableExtensionsDir = false;
    profiles.default = {
      extensions =
        with vscode-extensions;
        [
          ms-vscode-remote.remote-ssh
        ]
        ++ exts;
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
