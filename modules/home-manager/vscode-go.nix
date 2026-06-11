# vscode Go extension wiring for the nix-managed Go toolchain. Import from a
# user's vscode module, not from go.nix -- go.nix users are not assumed to be
# vscode users.
{ pkgs, ... }:
{
  # vscode-go resolves tools from <toolsGopath>/bin ahead of GOPATH/bin and
  # PATH, and targets installs there with GOBIN forced empty -- on a read-only
  # store path the extension's go-install fails loudly instead of silently
  # shadowing the nix-managed copies.
  programs.vscode.profiles.default.userSettings = {
    "go.toolsGopath" = "${pkgs.buildEnv {
      name = "vscode-go-tools";
      paths = import ./go-tools.nix pkgs;
    }}";
    "go.toolsManagement.autoUpdate" = false;
    "go.toolsManagement.checkForUpdates" = "local";
  };
}
