{ pkgs, inputs, ... }:
let
  # Load all extensions from nix-vscode package instead of nixpkgs, other than
  # remote-ssh, which has required special patches
  vscodeExts = inputs.nix-vscode-extensions.extensions.${pkgs.stdenv.hostPlatform.system};

  userSettings = import ./user-settings.nix;
  # everforest applies its settings by regenerating theme files inside its own
  # install dir at runtime, which is immutable here; pre-generate them (and its
  # first-run .flag) at build time with the config pinned in user-settings.nix
  everforestConfig = {
    darkContrast = "medium";
    lightContrast = "medium";
    darkWorkbench = "material";
    lightWorkbench = "material";
    darkSelection = "grey";
    lightSelection = "grey";
    darkCursor = "white";
    lightCursor = "black";
    italicKeywords = false;
    italicComments = true;
    diagnosticTextBackgroundOpacity = "0%";
    highContrast = false;
  } // (userSettings.everforest or { });
  everforest = vscodeExts.vscode-marketplace.sainnhe.everforest.overrideAttrs (old: {
    postInstall = (old.postInstall or "") + ''
      ext="$out/share/vscode/extensions/sainnhe.everforest"
      chmod -R u+w "$ext"
      mkdir -p "$TMPDIR/vscode-stub"
      echo 'module.exports = {};' > "$TMPDIR/vscode-stub/vscode.js"
      EXT="$ext" EF_CONFIG='${builtins.toJSON everforestConfig}' NODE_PATH="$TMPDIR/vscode-stub" \
        ${pkgs.nodejs}/bin/node ${./everforest-pregen.js}
    '';
  });

  exts =
    with vscodeExts;
    with vscode-marketplace;
    [
      bazelbuild.vscode-bazel
      bierner.markdown-mermaid
      # the pre-release channel this set tracks is stuck at 0.0.2 (grammar
      # only, no LSP); the release set carries the cue-lsp-enabled versions
      vscode-marketplace-release.cuelangorg.vscode-cue
      fcrespo82.markdown-table-formatter
      golang.go
      terrastruct.d2
      everforest
      maattdd.gitless
      ms-python.python
      jnoortheen.nix-ide
    ];
in
{
  imports = [ ../../../../modules/home-manager/vscode-go.nix ];

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
