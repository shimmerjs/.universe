# Default macOS system configuration applied to all macOS hosts. Additional
# modules are then layered on top or defined as part of the host definition.
{
  user,
  hostname,
  config,
  inputs,
  lib,
  pkgs,
  ...
}:
let
  gc = import ./nix-gc.nix { inherit lib pkgs; nixPackage = config.nix.package; };
in
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
  # (claude-code/codex freshness overlays are clod-scoped, not shared:
  # hosts/aw-chainguard/clod/overlays.nix, imported by that host only.)
  nixpkgs.overlays = [
    (final: prev: {
      qemu = inputs.nixpkgs-qemu.legacyPackages.${prev.stdenv.hostPlatform.system}.qemu;
      # nixos-unstable's vscode 1.129.1 predates the 1.129 asar-layout fix
      # (patchPhase chmods the pre-1.129 ripgrep path and fails on darwin).
      # Bridge from the master-tracking nixpkgs-claude input until the channel
      # catches up, then drop (check: main-nixpkgs vscode builds again).
      # Shared, not clod-scoped: every homie with programs.vscode hits it.
      # `import`, not legacyPackages: vscode is unfree and the input's default
      # package set is free-only.
      vscode =
        (import inputs.nixpkgs-claude {
          inherit (prev.stdenv.hostPlatform) system;
          config.allowUnfree = true;
        }).vscode;
    })
  ];

  # Used for backwards compatibility, please read the changelog before changing.
  # $ darwin-rebuild changelog
  system.stateVersion = 4;

  # nix-darwin master's HTML manual passes --toc-depth, which nixos-render-docs
  # on nixpkgs unstable (2026-07-05) removed in favor of --sidebar-depth, so the
  # darwin-manual-html derivation fails the whole closure. Two entry points,
  # both must go until nix-darwin catches up: the outer manual in
  # systemPackages (doc.enable), and darwin-uninstaller, which embeds its OWN
  # baseline darwin-system eval whose default documentation settings ignore
  # ours (why-depends: system-applications -> darwin-uninstaller ->
  # darwin-system -> system-path -> darwin-manual-html). Re-enable on a bump
  # where `nix build .#darwinConfigurations.<host>.system` passes with both on.
  documentation.doc.enable = false;
  system.tools.darwin-uninstaller.enable = false;

  # nix-darwin's collector is off on macOS: nix-collect-garbage aborts the whole
  # run on the first dead `.app` bundle it tries to chmod (App Management TCC),
  # skipping every remaining path and the .links sweep. ./nix-gc.nix does the
  # collect with those bundles filtered out, so leaving the builtin enabled
  # would only add a second job that fails weekly behind the same GC lock.
  # nix.gc.options stays declared in ../nix.nix for the NixOS hosts.
  nix.gc.automatic = lib.mkForce false;

  # The one weekly GC job: prune system-profile generations, then collect with
  # the .app filter. ProgramArguments (not command=) so nix-darwin skips its
  # /bin/sh -c wrapper and Login Items/BTM shows the script basename instead of
  # "sh"; the attr rename (nix-gc-system-profile -> nix-gc-weekly) is
  # deliberate -- BTM caches the display name per label, so only a fresh label
  # picks up the new name, and the job does more than the profile now.
  # Store-path executable is fine here: no RunAtLoad, and the store is long
  # mounted by the fire time. Std{Out,Error}Path because the failure this
  # replaces went to /dev/null every week for months.
  launchd.daemons.nix-gc-weekly = {
    serviceConfig.ProgramArguments = [ "${gc.collect}/bin/nix-gc-weekly" ];
    serviceConfig.RunAtLoad = false;
    serviceConfig.StandardOutPath = gc.logFile;
    serviceConfig.StandardErrorPath = gc.logFile;
    serviceConfig.StartCalendarInterval = {
      Hour = 3;
      Minute = 0;
      Weekday = 3;
    };
  };

  # Manual force collect, for when the weekly job is not the problem: it drops
  # the build graph the keep-* hops pin and the dev caches the store is not
  # responsible for. Dry-runs unless given --yes, self-elevates via sudo.
  environment.systemPackages = [ gc.force ];

  networking = {
    hostName = hostname;
    computerName = hostname;
  };

  programs.zsh.enable = true;
  # No system-level compinit: every user's home-manager zshrc runs its own
  # against the full fpath (plugins included), and a second compinit with a
  # different fpath fights over the same .zcompdump, forcing full rebuilds
  # per shell. enableCompletion stays on (it carries nix-zsh-completions);
  # bashcompinit stays on default -- it is a cheap guarantee for any
  # bash-style completer, not ours to trade away.
  programs.zsh.enableGlobalCompInit = false;

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
