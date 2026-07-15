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
      imports = [
        # darwin-side clod companion: fresh claude-code/codex overlay (the hm
        # module can't set overlays under useGlobalPkgs)
        ./clod/overlays.nix
      ];

      # Edge-glass screensaver hazard (khudson nix/edge-host.md, live-probed
      # 07-10): the armed no-modifier bottom-right hot corner let a corner tap
      # on the HUD glass start the screensaver, and caffeinate does not block
      # user-triggered screensavers. Disable the corner; pin mru-spaces off
      # while here (live on the machine but declared nowhere -- auto-rearrange
      # would shuffle the Space the Edge panel lives on). The Dock reads both
      # on its next restart.
      system.defaults.dock = {
        wvous-br-corner = 1;
        mru-spaces = false;
      };

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
        ./clod
        # khudson Edge HUD: host-scoped on purpose (edge-host.md review m2 --
        # this is the only host physically driving the Xeneon Edge).
        ../../homies/shimmerjs/home/khudson/nix/module.nix
      ];

      universe.home.khudson = {
        enable = true;
        # Daily-kitty RC socket + passwordless socket-only hardening. First
        # switch owes ONE manual daily-kitty quit+relaunch
        # (listen_on/allow_remote_control bind at startup only).
        mainKittyIntegration.enable = true;
      };

      # logiretch (MX Master 4) source on the bus: battery on logiretch.sock,
      # config-apply, and Options+ divert-reset on takeover. Sample config for
      # the user to tune; the setters are on-device UNVERIFIED and takeoverReset
      # is meaningful only after Options+ is uninstalled (with it installed both
      # HID++ masters share the vendor node and it will re-divert).
      universe.home.magicbus.modules.logiretch = true;
      universe.home.magicbus.logiretch = {
        dpi = 1600;
        takeoverReset = true;
      };

      # The screensaver hazard's second leg (edge-host.md): idleTime is a
      # ByHost domain nix-darwin CustomUserPreferences cannot set, so it rides
      # a -currentHost activation write. 0 = never; display sleep stays the
      # bus caffeinate's call, and the 600s idle screensaver blanked the
      # glass regardless of it.
      home.activation.screensaverIdleOff = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
        if [ "$(/usr/bin/defaults -currentHost read com.apple.screensaver idleTime 2>/dev/null || echo missing)" != "0" ]; then
          run /usr/bin/defaults -currentHost write com.apple.screensaver idleTime -int 0
        fi
      '';

      # Trust the chainguard tap so interactive brew commands (bundle, upgrade,
      # info) can load its formulae under Homebrew >=6 tap trust enforcement.
      # Must be a plain user-owned file, not a home.file symlink into the nix store:
      # Homebrew >=6 refuses to write its trust store when the containing dir isn't
      # owned by the current user, and a read-only store symlink trips that check.
      # An activation step installs it instead; the declared set wins on each switch.
      home.activation.homebrewTrust =
        let
          trust = pkgs.writeText "homebrew-trust.json" (builtins.toJSON {
            trustedtaps = [ "chainguard-dev/tap" ];
          });
        in
        lib.hm.dag.entryAfter [ "writeBoundary" ] ''
          $DRY_RUN_CMD mkdir -p "$HOME/.homebrew"
          $DRY_RUN_CMD rm -f "$HOME/.homebrew/trust.json"
          $DRY_RUN_CMD install -m600 ${trust} "$HOME/.homebrew/trust.json"
        '';

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
      programs.ssh.settings."github.com" = {
        AddKeysToAgent = "yes";
        IdentityFile = "~/.ssh/id_ed25519";
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
    };
}
