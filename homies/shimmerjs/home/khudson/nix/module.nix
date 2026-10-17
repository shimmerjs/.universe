# khudson home-manager module, imported host-scoped by
# hosts/aw-chainguard/default.nix (the Edge host is the only consumer)
# behind universe.home.khudson.enable; needs the one-time signing cert
# bootstrap (magicbus.nix, which owns the shared signing identity and the
# touchd daemon -- khudson requires it with both sources enabled). khudson
# deps fetch via vendorHash (no committed vendor tree); dev lifecycle runs
# in nix-shell from the khudson dir (shell.nix -> nix/devshell.nix).
{
  pkgs,
  lib,
  config,
  ...
}:
let
  cfg = config.universe.home.khudson;
  magicbus = config.universe.home.magicbus;

  # Runtime state root: macOS reaps /private/tmp entries idle
  # >~3 days, so no sockets or spools live there. Layout (three-process
  # topology: HUD kitty + scrape-substrate kitty + bus):
  #   khudson.sock        bus <-> dock/ctl
  #   touch.sock       touchd -> bus contact frames
  #   keys.sock        touchd -> bus Moonlander key events
  #   kitty-panel.sock HUD kitty RC (fullscreen Edge window; theme commands;
  #                    bound via --listen-on CLI without a PID suffix)
  #   r-<pid>-<n>.sock bus-owned renderer RC (scraping and input injection;
  #                    exists only while a connected dock needs exec widgets)
  #   main-kitty.sock  daily kitty RC (mainKittyIntegration, via LS launch
  #                    option -- CLI-verbatim like kitty-panel.sock above)
  #   claude/          claude-sessions spool (module-owned hooks, below)
  #   bin/             out-of-store khudson + touchd installs
  #   log/             agent stdout/stderr
  appSupport = "${config.home.homeDirectory}/Library/Application Support/khudson";

  # M1 treatment for khudson itself: dockmirror's direct AX walk and
  # the `ax unminimize` row verb (internal/ax) make the bus the
  # Accessibility TCC client, and a store-path client re-prompts on every
  # rebuild. hud-launcher execs this path too so the dock (spawned via
  # os.Executable) shares the stable identity. The identity string is
  # single-sourced in magicbus (it signs touchd too); referenced, never
  # duplicated.
  khudsonInstall = "${appSupport}/bin/khudson";
  renomanInstall = "${appSupport}/bin/renoman";
  khudsonSignRecipe = "sign-v1:${magicbus.signingIdentity}";

  # The verify+reinstall logic for both out-of-store installs, extracted so
  # the khudson-bininstall flake check (install-check.nix) exercises the
  # exact script activation runs.
  binInstallScript = import ./install-script.nix { inherit pkgs; };

  # Integrated host and standalone component binaries. Deps from go.mod/go.sum
  # (nix builds the go-modules FOD; no committed vendor tree -- recompute
  # vendorHash after any dep change: set lib.fakeHash, build, paste the got
  # hash). Only forced when cfg.enable.
  khudson = pkgs.buildGoModule {
    pname = "khudson";
    version = "0.1.0";
    src = ../khudson;
    vendorHash = "sha256-tHuTNfQ6gOGyzGT7bUfOiowboMc4Jl8C3lG/zx15O7M=";
    # Unit tests run in the build, not as flake checks. Tests that exec host
    # tools (top/vm_stat/m1ddc/gh/kitten) skip-on-missing; env-gated live
    # tests self-skip. The oryx
    # tests listen on loopback (httptest) -- fine while darwin builds run
    # sandbox-off; if nix sandboxing is ever enabled here, this derivation
    # needs __darwinAllowLocalNetworking = true.
    doCheck = true;
  };

  couchSession = pkgs.writeText "khudson-couch.kitty-session" ''
    new_tab Components
    layout grid
    launch --title Claude ${khudson}/bin/claudeman
    launch --title Keyboard ${khudson}/bin/kuiboard
    launch --title Logitech ${khudson}/bin/logiretch
    launch --title Resources ${khudson}/bin/khresources
    new_tab Shell
    launch
    focus_tab 1
  '';
  couchKitty = pkgs.writeText "khudson-couch-kitty.conf" ''
    include ${config.xdg.configHome}/kitty/kitty.conf
    startup_session ${couchSession}
    allow_remote_control no
  '';

  # Rebranded kitty.app copy (khudson.app + generated icns) so the HUD's
  # Dock tile and Cmd-Tab entry read "khudson"; the hud agent execs the
  # copy's .kitty-wrapped (see hud-app.nix for the identity mechanics).
  khudson-hud-app = pkgs.callPackage ./hud-app.nix { };

  # claude-sessions spool dir: <state root>/claude, matching what the
  # khudsonRuntimeDirs activation creates and what the Go claude-sessions
  # module reads (paths.ClaudeSpool()). One JSON per session, keyed by
  # session_id, atomically overwritten.
  claudeSpool = "${appSupport}/claude";

  # Live Edge config, shipped to ~/.config/khudson/edge.cue and passed via
  # -config to both the bus and the dock (through hud-launcher). Without it
  # both fall back to the embedded example and the composed home screen is
  # lost. Tool paths are
  # templated so the config never pins stale store paths by hand.
  edgeCUERendered = pkgs.replaceVars ./edge.cue {
    btop = "${pkgs.btop}/bin/btop";
    btopCfgDir = btopCfgDir;
    m1ddc = "${pkgs.m1ddc}/bin/m1ddc";
    spotatui = "/etc/profiles/per-user/${config.home.username}/bin/spotatui";
    claudeSpool = claudeSpool;
  };
  # Vet the rendered config against the schema embedded in this same khudson
  # build, so config/schema drift fails the closure at build time instead of
  # the bus dying (or example-falling-back) at runtime.
  edgeCUE =
    pkgs.runCommand "khudson-edge.cue"
      {
        nativeBuildInputs = [ khudson ];
      }
      ''
        khudson config vet ${edgeCUERendered}
        install -m 644 ${edgeCUERendered} $out
      '';

  # btop in the HUD runs against its own XDG_CONFIG_HOME (edge.cue argv) so
  # the panel profile never touches the user's btop config. The dir must be
  # writable (btop saves config and logs beside it), so activation seeds a
  # real directory from this store file instead of symlinking it in.
  btopCfgDir = "${appSupport}/btop-cfg";
  btopConf = pkgs.writeText "khudson-btop.conf" ''
    shown_boxes = "cpu"
    update_ms = 1000
  '';

  # Claude hooks run `khudson hook <event>` -- one static-binary fork per
  # fire (the bash+jq scripts this replaced forked 4-9 children at a
  # measured ~65-70ms/fire; the hook surface fires per turn-class event on
  # every session). Event semantics -- merge-don't-clobber, attention
  # set/clear, SessionEnd retention + 7d reaper, KITTY_WINDOW_ID plant --
  # live in khudson/internal/hookspool with its own test suite.
  hookCmd = event: "${khudson}/bin/khudson hook -dir ${lib.escapeShellArg claudeSpool} ${event}";

  # Agents are module-shipped plists, NOT home-manager launchd.agents: that
  # module force-wraps every command in `/bin/sh -c '/bin/wait4path /nix/store
  # && exec ...'` (mutateConfig, no opt-out), and Login Items/BTM names an
  # item after the basename of ProgramArguments[0] -- so every agent displayed
  # as "sh". ProgramArguments[0] is instead a
  # named launcher script under the state root: out-of-store because BTM keys
  # the item's identity on the executable path, so a store-path launcher would
  # churn the record (and its added-notification) on every rebuild. The
  # launcher preamble keeps the /nix/store guard the HM wrapper provided;
  # per-agent wait4path on the install paths (touchd, khudson) still parks a
  # fresh boot (or fresh host) instead of crash-looping into launchd
  # throttling. Labels are org.khudson.* -- fresh labels on
  # purpose: BTM caches the display name per registration, so only retiring
  # the org.nix-community.home.* records (setupLaunchAgents boots them out and
  # deletes their plists once they leave launchd.agents) sheds the cached
  # "sh" names.
  agentsDir = "${appSupport}/agents";
  agentLabel = name: "org.khudson.${name}";
  mkLauncher =
    name: cmd:
    pkgs.writeText "khudson-${name}-launcher" ''
      #!/bin/sh
      /bin/wait4path /nix/store
      ${cmd}
    '';
  mkAgentPlist =
    name:
    pkgs.writeText "${agentLabel name}.plist" (
      lib.generators.toPlist { escape = true; } (
        {
          Label = agentLabel name;
          ProgramArguments = [ "${agentsDir}/khudson-${name}" ];
          # launchd's default PATH is /usr/bin:/bin:/usr/sbin:/sbin; the bus's
          # native modules LookPath nix-profile binaries (gh, m1ddc, kitten) and
          # exec-widget argvs are bare names (btop) -- without the nix dirs every
          # one of them fails to resolve.
          EnvironmentVariables.PATH =
            "/etc/profiles/per-user/${config.home.username}/bin:/run/current-system/sw/bin:/usr/bin:/bin:/usr/sbin:/sbin";
          RunAtLoad = true;
          KeepAlive = true;
          ProcessType = "Interactive";
          StandardOutPath = "${appSupport}/log/${name}.log";
          StandardErrorPath = "${appSupport}/log/${name}.log";
        }
        # hud only: the launcher persists its own relaunch backoff, but a lost
        # state file must not let a KeepAlive respawn storm tight-loop the
        # supervisor (the 2026-07-14 HUD stack).
        // lib.optionalAttrs (name == "hud") { ThrottleInterval = 30; }
      )
    );

  # The input daemon (touchd) is magicbus's agent now (magicbus.nix); it owns
  # the Input Monitoring TCC grant and emits contact frames on touch.sock
  # and Moonlander key events on keys.sock.
  agentCommands = {
    reno = ''/bin/wait4path "${renomanInstall}" && exec "${renomanInstall}" serve'';
    # The HUD kitty, display-gated: khudson hud-launcher launches the plain
    # fullscreen window (--position + --start-as fullscreen) ONLY while the
    # Edge is connected -- a blind launch clamps a junk non-fullscreen
    # window onto the remaining display. The launcher
    # computes --position from NSScreen at launch time (arrangement is not
    # a constant), tears the window down if the display disconnects, and
    # relaunches with backoff. Quick-access panels are dead: they refuse
    # fullscreen ("desktop panel cannot be made fullscreen") and their
    # invisible windows never paint. NO macos_hide_from_tasks on this
    # instance: accessory apps cannot enter native fullscreen (window
    # wedges tiny and off-screen); the Dock tile is the price. AMFI
    # behavior under launchd exec (vs Launch Services) is a spike 3 exit
    # criterion; if it dies, the fallback is launching the
    # signed ~/Applications bundle, with KeepAlive semantics re-verified.
    # Execs the fixed install, not the store path (M1, khudsonInstall):
    # the dock runs os.Executable() inside the HUD kitty, so the stable
    # TCC identity has to start here. wait4path parks a fresh boot (or
    # fresh host) until khudsonBinInstall has run once.
    hud = ''/bin/wait4path "${khudsonInstall}" && exec "${khudsonInstall}" hud-launcher -kitty "${khudson-hud-app}/Applications/khudson.app/Contents/MacOS/.kitty-wrapped" -kitty-config "${config.xdg.configHome}/khudson/hud-kitty.conf" -config "${config.xdg.configHome}/khudson/edge.cue"'';

    # Headless bus: recognizer, scheduler, RC client, widget supervision.
    # The Accessibility TCC client (dockmirror's direct AX walk plus the
    # `ax unminimize` row verb), so it execs the fixed install like the
    # hud agent above.
    bus = ''/bin/wait4path "${khudsonInstall}" && exec "${khudsonInstall}" bus -renderer-kitty "${khudson-hud-app}/Applications/khudson.app/Contents/MacOS/.kitty-wrapped" -config "${config.xdg.configHome}/khudson/edge.cue"'';
  };
in
{
  # khudson depends DOWN on the magicbus fabric (it consumes touch.sock +
  # keys.sock); it does NOT own any device's app config, so mxmaster.nix is
  # imported by the host, not here.
  imports = [ ./magicbus.nix ];

  options.universe.home.khudson = {
    enable = lib.mkEnableOption "khudson Edge HUD";

    # The signing identity + touchd daemon options moved to magicbus.nix
    # (universe.home.magicbus), the single source for both.

    # The claude-sessions spool is the only khudson piece that touches the
    # clod config (it contributes claude-code hooks). Split behind its own
    # toggle so the HUD can run without the claude integration (or vice
    # versa), but default it on with the module so `enable` still lights up
    # the whole thing.
    claudeSpool.enable = lib.mkOption {
      type = lib.types.bool;
      default = cfg.enable;
      defaultText = lib.literalExpression "config.universe.home.khudson.enable";
      description = "Populate the claude-sessions spool via module-owned claude-code hooks (UserPromptSubmit + SessionStart + SessionEnd + Notification + Stop + StopFailure).";
    };

    # Fold the main-kitty RC integration into the daily kitty config
    # (main-kitty-integration.md). Off by default even when the module is
    # enabled: it mutates programs.kitty (a shared, always-on config) and
    # owes one manual daily-kitty relaunch (listen_on binds at startup), so
    # it is opt-in rather than riding cfg.enable.
    mainKittyIntegration.enable = lib.mkEnableOption "declarative main-kitty RC socket + socket-only hardening for the khudson bus (see main-kitty-integration.md)";

    couchMode.enable = lib.mkOption {
      type = lib.types.bool;
      default = cfg.mainKittyIntegration.enable;
      defaultText = lib.literalExpression "config.universe.home.khudson.mainKittyIntegration.enable";
      description = "Open independent component programs in kitty's existing Quick Access terminal startup session.";
    };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.couchMode.enable {
      assertions = [{
        assertion = cfg.enable && config.programs.kitty.enable;
        message = "khudson couch mode requires khudson and kitty packages.";
      }];
      programs.kitty.quickAccessTerminalConfig.kitty_conf = "${couchKitty}";
    })
    (lib.mkIf cfg.enable {
    # The HUD needs both HID sources feeding the bus: touch.sock (edge) and
    # keys.sock (moonlander) come from magicbus's daemon. mkDefault so a host
    # could still override explicitly, with the assertion catching a cut
    # that would silently starve the bus.
    universe.home.magicbus = {
      enable = lib.mkDefault true;
      modules.edge = lib.mkDefault true;
      modules.moonlander = lib.mkDefault true;
    };
    assertions = [
      {
        assertion = magicbus.enable && magicbus.modules.edge && magicbus.modules.moonlander;
        message = "universe.home.khudson.enable requires universe.home.magicbus with modules.edge and modules.moonlander enabled: the bus consumes touch.sock and keys.sock.";
      }
    ];

    # ffmpeg backs the reno clip verbs (trim/stitch/mute/dub); mpv is the
    # scrub/review surface. Both PATH-resolve from the profile (nixpkgs
    # darwin ships them; OBS itself is a homebrew cask).
    home.packages = [
      khudson
      pkgs.ffmpeg
      pkgs.mpv
    ];

    # hud kitty instance config; khudson-scoped so the daily kitty config is
    # untouched. The main-kitty side (fixed RC socket, passwordless
    # socket-only) is folded into programs.kitty under
    # cfg.mainKittyIntegration.enable below. The theme is included from the
    # same kitty-themes package the daily kitty uses, so the two instances
    # cannot drift apart again.
    xdg.configFile."khudson/hud-kitty.conf".source = pkgs.replaceVars ./hud-kitty.conf {
      everforestTheme = "${pkgs.kitty-themes}/share/kitty-themes/themes/everforest_dark_soft.conf";
    };
    xdg.configFile."khudson/edge.cue".source = edgeCUE;

    # Install the named launchers and ship each agent plist (see the agentsDir
    # comment for why this bypasses launchd.agents). Launchers reinstall
    # unconditionally -- content may embed new store paths (kitty) while the
    # plist, which only names the stable launcher path, stays byte-identical;
    # retire the current owners before bootstrapping their replacements.
    # Unloaded agents are repaired even when the plist is unchanged.
    home.activation.khudsonAgents =
      lib.hm.dag.entryBetween
        [ "khudsonRestart" ]
        [
          "setupLaunchAgents"
          "khudsonBinInstall"
          "khudsonTouchdInstall"
        ]
        ''
          run install -d -m 700 "${agentsDir}"
          khudsonAgentsUid=$(id -u)
          # The previous bus owned Reno state. Retire both possible owners
          # before bootstrapping either replacement, including first migration.
          ${builtins.readFile ./agent-lifecycle.sh}
          khudsonRetireOwner "${agentLabel "bus"}" || exit 1
          khudsonRetireOwner "${agentLabel "reno"}" || exit 1
          khudsonRetireOwner "${agentLabel "hud"}" || exit 1
          # The bus now owns the renderer. Retire the previous always-on agent.
          if /bin/launchctl print "gui/$khudsonAgentsUid/${agentLabel "substrate"}" >/dev/null 2>&1; then
            run /bin/launchctl bootout "gui/$khudsonAgentsUid/${agentLabel "substrate"}" || exit 1
          fi
          run rm -f "${config.home.homeDirectory}/Library/LaunchAgents/${agentLabel "substrate"}.plist" "${agentsDir}/khudson-substrate"
          ${lib.concatStrings (
            lib.mapAttrsToList (name: cmd: ''
              run install -m 755 "${mkLauncher name cmd}" "${agentsDir}/khudson-${name}"
              if ! /usr/bin/cmp -s "${mkAgentPlist name}" "${config.home.homeDirectory}/Library/LaunchAgents/${agentLabel name}.plist"; then
                run /bin/launchctl bootout "gui/$khudsonAgentsUid/${agentLabel name}" 2>/dev/null || true
                run install -m 444 "${mkAgentPlist name}" "${config.home.homeDirectory}/Library/LaunchAgents/${agentLabel name}.plist"
                run /bin/launchctl bootstrap "gui/$khudsonAgentsUid" "${config.home.homeDirectory}/Library/LaunchAgents/${agentLabel name}.plist" || exit 1
              elif ! /bin/launchctl print "gui/$khudsonAgentsUid/${agentLabel name}" > /dev/null 2>&1; then
                run /bin/launchctl bootstrap "gui/$khudsonAgentsUid" "${config.home.homeDirectory}/Library/LaunchAgents/${agentLabel name}.plist" || exit 1
              fi
            '') agentCommands
          )}
        '';

    # Ordered activation pipeline:
    # config flip (linkGeneration, implicit)
    #   -> khudsonRuntimeDirs (+ magicbusRuntimeDirs, magicbus.nix)
    #   -> khudsonBinInstall + khudsonTouchdInstall (magicbus.nix)
    #                             (explicitly before setupLaunchAgents, M1d)
    #   -> setupLaunchAgents      (home-manager: retires the old
    #                              org.nix-community.home.* agents)
    #   -> khudsonAgents          (module-owned: launchers + plists + bootstrap;
    #                              magicbusAgents owns the touchd agent)
    #   -> khudsonRestart         (bus/Reno readiness; no second restart)
    # Accepted cost: any switch touching khudson restarts the whole HUD,
    # including keepAlive scraped panes.

    home.activation.khudsonRuntimeDirs = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      # -m 700 every time: BSD install -d chmods a pre-existing directory to
      # its default 0755, so omitting the mode RE-OPENS the state root (and
      # its sockets) to world-read on every switch
      run install -d -m 700 "${appSupport}" "${appSupport}/bin" "${appSupport}/claude" "${appSupport}/log" "${appSupport}/reno" "${appSupport}/reno/runs" "${appSupport}/reno/log" "${appSupport}/reno/workspace" "${btopCfgDir}/btop"
      # re-seed every switch: btop rewrites its config on exit, so activation
      # restores the declared baseline rather than letting it drift
      run install -m 644 "${btopConf}" "${btopCfgDir}/btop/btop.conf"
    '';

    # khudson gets the same copy-out-and-sign as touchd (see magicbus.nix's
    # khudsonTouchdInstall for the staging discipline and the
    # no-hardened-runtime rationale, which applies verbatim: khudson links
    # nix-store dylibs too). No .updated
    # marker: khudsonAgents replaces service lifetimes on activation. The script
    # (install-script.nix) stages + signs BESIDE the granted binary and swaps
    # atomically only after codesign succeeds (M1c), verifies the installed
    # signature on EVERY activation (the stamp is only the store-path/recipe
    # fast-path, never a verify bypass), and reinstalls loudly on failure.
    home.activation.khudsonBinInstall =
      lib.hm.dag.entryBetween [ "setupLaunchAgents" ] [ "khudsonRuntimeDirs" ]
        ''
          run ${binInstallScript}/bin/khudson-bin-install \
            "${khudson}/bin/khudson" \
            "${khudsonInstall}" \
            "${appSupport}/bin/.khudson.store-path" \
            "${khudson} ${khudsonSignRecipe}" \
            ${lib.escapeShellArg magicbus.signingIdentity}
          run ${binInstallScript}/bin/khudson-bin-install \
            "${khudson}/bin/renoman" \
            "${renomanInstall}" \
            "${appSupport}/bin/.renoman.store-path" \
            "${khudson} ${khudsonSignRecipe}" \
            ${lib.escapeShellArg magicbus.signingIdentity}
        '';

    # Agents were retired and bootstrapped above. Check readiness without
    # killing the replacement processes or requiring an attached Edge.
    home.activation.khudsonRestart = lib.hm.dag.entryAfter [ "setupLaunchAgents" ] ''
      khudsonWaitSock() {
        [ -n "''${DRY_RUN:-}" ] && return 0
        _tries=50
        while [ "$_tries" -gt 0 ]; do
          [ -S "$1" ] && return 0
          /bin/sleep 0.2
          _tries=$((_tries - 1))
        done
        echo "khudson: timed out waiting for $1" >&2
        return 1
      }

      khudsonWaitSock "${appSupport}/khudson.sock" || exit 1
      khudsonWaitSock "${appSupport}/reno/owner.sock" || exit 1
      run "${khudson}/bin/khudson" ctl status >/dev/null || exit 1
      run "${khudson}/bin/renoman" jobs >/dev/null || exit 1
    '';
    })

    # claude-sessions spool: module-owned claude-code hooks. Contributed via
    # programs.claude-code.settings.hooks and deep-merged with clod's hooks
    # attrset (clod owns PreToolUse/PostToolUse and its own Stop entry).
    # Sharing the Stop key is safe: the freeform-JSON settings type merges
    # same-key hook LISTS by concatenation (types.listOf semantics), so
    # clod's gocheck and the spool-stop entry below both survive -- verified
    # by nix eval of the merged settings.hooks.Stop. The runtime dir this
    # writes to (claudeSpool) is created by khudsonRuntimeDirs when
    # cfg.enable, so run claudeSpool.enable alongside enable (its default).
    (lib.mkIf cfg.claudeSpool.enable {
      programs.claude-code.settings.hooks = {
        UserPromptSubmit = [
          {
            hooks = [
              {
                type = "command";
                command = hookCmd "prompt";
              }
            ];
          }
        ];
        SessionEnd = [
          {
            hooks = [
              {
                type = "command";
                command = hookCmd "end";
              }
            ];
          }
        ];
        Notification = [
          {
            hooks = [
              {
                type = "command";
                command = hookCmd "notify";
              }
            ];
          }
        ];
        Stop = [
          {
            hooks = [
              {
                type = "command";
                command = hookCmd "stop";
              }
            ];
          }
        ];
        StopFailure = [
          {
            hooks = [
              {
                type = "command";
                command = hookCmd "stopfail";
              }
            ];
          }
        ];
        SessionStart = [
          {
            hooks = [
              {
                type = "command";
                command = hookCmd "start";
              }
            ];
          }
        ];
      };

      # The spool hook mkdir -p's the dir, but claudeSpool.enable can run
      # without cfg.enable (which owns khudsonRuntimeDirs); ensure the dir
      # exists and is 0700 here too so the two toggles are independent.
      home.activation.khudsonClaudeSpoolDir = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
        run install -d -m 700 "${appSupport}" "${claudeSpool}"
      '';
    })

    # main-kitty RC integration (main-kitty-integration.md), declaratively
    # folded in where possible. allow_remote_control is redefined (yes ->
    # socket-only) so it needs mkForce over the daily kitty's value; listen_on
    # and the quick-access RC-off override are new keys. Caveat, unchanged:
    # listen_on binds only at kitty startup, so one manual daily-kitty
    # relaunch is owed after first enabling.
    (lib.mkIf cfg.mainKittyIntegration.enable {
      # The listen_on socket lives under the khudson state root, which only
      # khudsonRuntimeDirs (gated on cfg.enable) creates; standalone the daily
      # kitty would bind into a missing directory.
      assertions = [
        {
          assertion = cfg.enable;
          message = "universe.home.khudson.mainKittyIntegration.enable requires universe.home.khudson.enable: the RC socket path is under the khudson state root, created only by the enabled module's activation.";
        }
      ];
      programs.kitty = {
        settings = {
          # RC only via the fixed socket below; tty RC is refused outright,
          # and socket peers are trusted by the socket file's user-write-only
          # mode. That mode never consults remote_control_password, so the
          # old password include was inert on this path and retired (see
          # main-kitty-integration.md).
          allow_remote_control = lib.mkForce "socket-only";
        };
        # Fixed, non-pid socket, delivered as a LAUNCH option, not settings:
        # config-form unix listen_on gets "-<PID>" appended (kitty
        # expand_listen_on, main.py ~365-371 in 0.47.4 -- same semantics the
        # panel socket comment above documents), so only CLI --listen-on
        # binds the exact path the bus contract promises.
        # The launcher shlex-parses macos-launch-services-cmdline and
        # home-manager joins this list with bare spaces, so the space in
        # "Application Support" needs the embedded quotes. Caveat: the file is
        # read only for Launch-Services launches -- a shell-spawned kitty
        # binds no socket (acceptable: the daily kitty is LS-launched).
        # kitty knows it was LS-launched only from the
        # KITTY_LAUNCHED_BY_LAUNCH_SERVICES=1 in its bundle's LSEnvironment,
        # which modules/darwin/kitty-overlay.nix must merge into, not replace;
        # posture-check leg (c) pins both halves.
        darwinLaunchOptions = [
          "--listen-on"
          "'unix:${appSupport}/main-kitty.sock'"
        ];
        # The quick-access terminal is a second kitty process on the same
        # kitty.conf; RC off means it never binds listen_on, so it cannot
        # squat or shadow the fixed socket and exposes no second RC surface.
        quickAccessTerminalConfig.kitty_override = "allow_remote_control=no";
      };
    })
  ];
}
