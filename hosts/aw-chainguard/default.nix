# Work macbook.
{
  system = "aarch64-darwin";
  user = "shimmerjs";
  homie = import ../../homies/shimmerjs;

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
        ];
        brews = [
          "gitsign"
          "chainctl"
          "melange"
          "helm" # :[
        ];
        casks = [
          "orbstack"
          "google-chrome"
        ];
      };
    };

  home =
    {
      config,
      lib,
      pkgs,
      user,
      inputs,
      ...
    }:
    {
      imports = [
        ../../modules/home-manager/gcloud.nix
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
      programs.ssh.matchBlocks."github.com" = {
        extraOptions.AddKeysToAgent = "yes";
        identityFile = "~/.ssh/id_ed25519";
      };

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
              ${user}:
                  git_protocol: ssh
          user: ${user}
      '';

      programs.vscode.profiles.default = with pkgs; {
        extensions =
          with inputs.nix-vscode-extensions.extensions.${pkgs.stdenv.hostPlatform.system};
          with vscode-marketplace;
          [ hashicorp.terraform ];
      };

      programs.claude-code = {
        enable = true;
        settings = {
          effortLevel = "max";
          model = "claude-opus-4-6";
          statusLine = {
            type = "command";
            command = "~/.claude/statusline.sh";
            refreshInterval = 10;
          };
          enabledPlugins = {
            "gopls-lsp@claude-plugins-official" = true;
          };

          permissions = {
            allow = [
              "Bash(bash:*)"
              "Bash(cargo check:*)"
              "Bash(chmod +x:*)"
              "Bash(curl:*)"
              "Bash(file:*)"
              "Bash(find:*)"
              "Bash(git:*)"
              "Bash(d2:*)"
              "WebFetch(domain:d2lang.com)"
              "WebFetch(domain:terrastruct.com)"
              "Bash(go build:*)"
              "Bash(go doc:*)"
              "Bash(go mod:*)"
              "Bash(go test:*)"
              "Bash(go version:*)"
              "Bash(go vet:*)"
              "Bash(gh api:*)"
              "Bash(gh issue:*)"
              "Bash(gh pr:*)"
              "Bash(gh search:*)"
              "Bash(grep:*)"
              "Bash(log show:*)"
              "Bash(ls:*)"
              "Bash(lsof:*)"
              "Bash(nix:*)"
              "Bash(nix build:*)"
              "Bash(nix eval:*)"
              "Bash(nix flake:*)"
              "Bash(nix-shell:*)"
              "Bash(open:*)"
              "Bash(pkill:*)"
              "Bash(python3:*)"
              "Bash(sysctl security:*)"
              "Bash(wc:*)"
              "Bash(xargs sh:*)"
              "WebFetch(domain:discourse.nixos.org)"
              "WebFetch(domain:docs.rs)"
              "WebFetch(domain:github.com)"
              "WebFetch(domain:localhost)"
              "WebFetch(domain:mynixos.com)"
              "WebFetch(domain:nixos.org)"
              "WebFetch(domain:private-user-images.githubusercontent.com)"
              "WebFetch(domain:raw.githubusercontent.com)"
              "WebFetch(domain:wiki.nixos.org)"
              "WebSearch"
              "mcp__claude_ai_Chainguard_Analytics__metrics"
            ];
          };

          env = {
            CLAUDE_CODE_ENABLE_TELEMETRY = 0;
          };

          spinnerVerbs = {
            mode = "replace";
            verbs = [
              "pillagin'"
              "cookin'"
              "wildin'"
              "burnin'"
              "usurping"
              "scheming"
              "rippin"
              "tokin"
              "trippin"
              "disassociating"
              "spittin"
              "commoditizin"
              "overthrowing"
            ];
          };
        };
      };
      home.file.".claude/statusline.sh" = {
        executable = true;
        source = ./claude-statusline.sh;
      };
    };
}
