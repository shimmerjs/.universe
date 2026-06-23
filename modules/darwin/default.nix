# Default macOS system configuration applied to all macOS hosts. Additional
# modules are then layered on top or defined as part of the host definition.
{ user, hostname, config, inputs, ... }:
{
  imports = [
    ../nix.nix
    ../universe.nix
  ];

  # Pin qemu to 10.2.2 (flake input nixpkgs-qemu). nixpkgs master ships qemu
  # 11.0.0, whose HVF backend asserts at vCPU init on Apple Silicon
  # (HV_SYS_REG_SMCR_EL1) and cannot start a guest; 10.2.2 is the newest
  # pre-regression release and is HVF-working here. Lives in the shared darwin
  # module so every macOS host that virtualizes (qemu in its package set) gets
  # the working build. Remove with the flake input once nixpkgs has a fix.
  nixpkgs.overlays = [
    (final: prev: {
      qemu = inputs.nixpkgs-qemu.legacyPackages.${prev.stdenv.hostPlatform.system}.qemu;
    })
    # claude-code from a fresh nixpkgs (flake input nixpkgs-claude, tracks master):
    # the main nixpkgs is the cache-warm-but-lagging nixos-unstable, which trails
    # claude-code releases. Override just this one package so clod stays current;
    # bump it with `nix flake update nixpkgs-claude`. We `import` (not
    # legacyPackages) to set allowUnfree -- claude-code is unfree and the input's
    # default package set is free-only, unlike our main nixpkgs (modules/nix.nix).
    (final: prev: {
      claude-code = (import inputs.nixpkgs-claude {
        inherit (prev.stdenv.hostPlatform) system;
        config.allowUnfree = true;
      }).claude-code;
    })
  ];

  # Used for backwards compatibility, please read the changelog before changing.
  # $ darwin-rebuild changelog
  system.stateVersion = 4;

  nix = {
    # GC configuration that is specific to nix-darwin
    gc = {
      interval = {
        Hour = 3;
        Minute = 0;
        Weekday = 3;
      };
    };
  };

  # nix.gc runs nix-collect-garbage, which prunes only the invoking user's
  # profiles -- never the nix-darwin `system` profile. Old system generations
  # therefore accumulate as live GC roots and pin their whole closures, leaving
  # the collector nothing to free. Prune them 15 min before the 03:00 collect so
  # the newly-unreferenced paths get swept the same run. Window mirrors
  # nix.gc.options (--delete-older-than 30d). Always keeps the current generation.
  launchd.daemons.nix-gc-system = {
    command = "${config.nix.package}/bin/nix-env --profile /nix/var/nix/profiles/system --delete-generations 30d";
    serviceConfig.RunAtLoad = false;
    serviceConfig.StartCalendarInterval = {
      Hour = 2;
      Minute = 45;
      Weekday = 3;
    };
  };

  networking = {
    hostName = hostname;
    computerName = hostname;
  };

  programs.zsh.enable = true;

  users.users.${user} = {
    name = user;
    # Explicitly set up user home directory to workaround nix-darwin issue:
    # https://github.com/LnL7/nix-darwin/issues/423
    home = "/Users/${user}";
  };

  security = {
    pam = {
      services = {
        sudo_local = {
          touchIdAuth = true;
        };
      };
    };
  };

  # Automatically apply macOS preference changes without requiring login/logout
  system.activationScripts.postActivation.text = ''
    # Following line should allow us to avoid a logout/login cycle
    # TODO: Use username parameter, dont poison root darwin module
    sudo -u ${user} /System/Library/PrivateFrameworks/SystemAdministration.framework/Resources/activateSettings -u
  '';
}
