{ inputs, ... }:
{ config, lib, pkgs, ... }:
{
  imports = [
    ../../modules/home-manager
    ../../modules/home-manager/darwin
    ../../modules/home-manager/kitty
    ../../modules/home-manager/vscode
    ../../modules/home-manager/gcloud
  ];

  home.packages = with pkgs; [
    terraform
    k3d
  ];

  # Work-specific git config
  programs.git = {
    includes = [
      {
        condition = "gitdir:~/dev/cg/"; # All chainguard repos must be signed
        contents = {
          user = {
            email = "alex.weidner@chainguard.dev";
          };
          commit = {
            gpgSign = true;
          };
          tag = {
            gpgSign = true;
          };
          gpg = {
            x509 = {
              # Installed via `brew` in ./configuration.nix
              program = "gitsign";
            };
            format = "x509";
          };
          # https://docs.sigstore.dev/signing/gitsign/#file-config
          gitsign = {
            connectorID = "https://accounts.google.com";
            autocloseTimeout = "1s";
          };
        };
      }
    ];
  };

  programs.ssh.extraConfig = ''
    Host github.com
      AddKeysToAgent yes
      IdentityFile ~/.ssh/id_ed25519
  '';

  programs.gh = {
    enable = true;
    settings = {
      git_protocol = "ssh";
    };
  };
}
