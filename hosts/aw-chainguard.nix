# Work macbook.
{
  system = "aarch64-darwin";
  user = "shimmerjs";
  homie = import ../homies/shimmerjs;

  systemConfig = { config, lib, pkgs, ... }: {
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

  home = { config, lib, pkgs, ... }: {
    imports = [
      ../modules/home-manager/gcloud.nix
    ];

    home.packages = with pkgs; [
      terraform
      k3d
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

      aliases = {
        cs = "commit -s";
      };
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

    programs.gh-dash = {
      enable = true;
      settings = {
        prSections = [
          {
            title = "prs/team";
            filters = "is:open author:joshrwolf";
          }
          {
            title = "prs/review-requested";
            filters = "is:open review-requested:@me";
          }
          {
            title = "prs/mine";
            filters = "is:open author:@me";
            layout = {
              author = {
                hidden = true;
              };
            };
          }
        ];
        defaults = {
          preview = {
            width = 80;
          };
        };
        repoPaths = {
          "chainguard-*/*" = "~/dev/cg/chainguard-*/*";
          "wolfi-dev/*" = "~/dev/cg/wolfi-dev/*";
        };
        # Based on Everforest Light Medium, to be consistent with Kitty + VSCode
        # https://github.com/sainnhe/everforest/blob/master/palette.md#light
        theme = {
          colors = {
            text = {
              primary = "#5C6A72";
              secondary = "#3A94C5";
              inverted = "#708089";
              faint = "#939F91";
              warning = "#DFA000";
              success = "#93B259";
            };
            background = {
              selected = "#E6E2CC";
            };
            border = {
              primary = "#829181";
              secondary = "#A6B0A0";
              faint = "#BDC3AF";
            };
          };
        };
      };
    };
  };
}
