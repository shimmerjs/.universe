# Work macbook.
{
  system = "aarch64-darwin";
  user = "shimmerjs";
  homie = import ../homies/shimmerjs;

  systemConfig =
    {
      config,
      lib,
      pkgs,
      ...
    }:
    {
      homebrew = {
        taps = [
          "chainguard-dev/tap"
          "minamijoyo/hcledit"
        ];
        brews = [
          "gitsign"
          "chainctl"
          "melange"
          "hcledit"
          "helm" # :[
        ];
        casks = [
          "google-chrome"
        ];
      };
    };

  home =
    {
      config,
      lib,
      pkgs,
      ...
    }:
    {
      imports = [
        ../modules/home-manager/gcloud.nix
      ];

      home.packages = with pkgs; [
        terraform
        k3d
        qemu
        yq-go
        hyperfine
        sqlite
        sqlite-utils
        gum
        samply
      ];

      programs.git = {
        includes = [
          # Only modify Git config for work repositories.
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

      # Set up SSH key for github.com authentication
      programs.ssh.extraConfig = ''
        Host github.com
          AddKeysToAgent yes
          IdentityFile ~/.ssh/id_ed25519
      '';

      # Configure `gh` CLI to use ssh when setting up repositories
      programs.gh = {
        enable = true;
        settings = {
          git_protocol = "ssh";
        };
      };
      # TODO: patch home-manager to support defining host configuration
      # or at least generate from root settings, gh config story is dumb
      # enough to drop it
      home.file."${config.xdg.configHome}/gh/hosts.yml".text = ''
        github.com:
          git_protocol: ssh
          users:
              shimmerjs:
                  git_protocol: ssh
          user: shimmerjs
      '';
    };
}
