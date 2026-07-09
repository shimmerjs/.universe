# khudson home-manager module, imported host-scoped by
# hosts/aw-chainguard/default.nix (the Edge host is the only consumer)
# behind universe.home.khudson.enable; needs the one-time signing cert
# bootstrap below. khudson/touchd deps fetch via vendorHash (no committed
# vendor tree); dev lifecycle runs in nix-shell from the khudson dir
# (shell.nix -> nix/devshell.nix).
{
  pkgs,
  lib,
  config,
  ...
}:
let
  cfg = config.universe.home.khudson;

  # Runtime state root: macOS reaps /private/tmp entries idle
  # >~3 days, so no sockets or spools live there. Layout (three-process
  # topology: HUD kitty + scrape-substrate kitty + bus):
  #   khudson.sock        bus <-> dock/ctl
  #   touch.sock       touchd -> bus contact frames
  #   keys.sock        touchd -> bus Moonlander key events
  #   kitty-panel.sock HUD kitty RC (fullscreen window on the Edge; bus
  #                    injects input here; bound via --listen-on CLI, which
  #                    is verbatim -- the config form appends -PID)
  #   kitty.sock       scrape-substrate kitty RC (windowless instance hosting
  #                    scraped TUIs; bus supervises/scrapes here)
  #   main-kitty.sock  daily kitty RC (mainKittyIntegration, via LS launch
  #                    option -- CLI-verbatim like kitty-panel.sock above)
  #   claude/          claude-sessions spool (module-owned hooks, below)
  #   bin/             out-of-store khudson + touchd installs
  #   log/             agent stdout/stderr
  appSupport = "${config.home.homeDirectory}/Library/Application Support/khudson";

  # Fixed out-of-store install path: the Input Monitoring grant
  # keys on the binary's path + signature, so the granted binary must never
  # move with the store generation.
  touchdInstall = "${appSupport}/bin/khudson-touchd";

  # Joins the install stamp beside the store path; bump the version whenever
  # the codesign invocation in khudsonTouchdInstall changes so existing
  # installs get re-signed on the next switch.
  touchdSignRecipe = "sign-v2:${cfg.signingIdentity}";

  # Same M1 treatment for khudson itself: dockmirror's direct AX walk and
  # the `ax unminimize` row verb (internal/ax) make the bus the
  # Accessibility TCC client, and a store-path client re-prompts on every
  # rebuild. hud-launcher execs this path too so the dock (spawned via
  # os.Executable) shares the stable identity.
  khudsonInstall = "${appSupport}/bin/khudson";
  khudsonSignRecipe = "sign-v1:${cfg.signingIdentity}";

  # The verify+reinstall logic for both out-of-store installs, extracted so
  # the khudson-bininstall flake check (install-check.nix) exercises the
  # exact script activation runs.
  binInstallScript = import ./install-script.nix { inherit pkgs; };

  # khudson: bus + dock + ctl, one binary. Deps fetched from go.mod/go.sum
  # (nix builds the go-modules FOD; no committed vendor tree -- recompute
  # vendorHash after any dep change: set lib.fakeHash, build, paste the got
  # hash). Only forced when cfg.enable.
  khudson = pkgs.buildGoModule {
    pname = "khudson";
    version = "0.1.0";
    src = ../khudson;
    vendorHash = "sha256-hSVOdtjBWfU3UvU29PnUWyrAArrUujFoIT30HVc2f/g=";
    # Unit tests run in the build, not as flake checks. Tests that exec host
    # tools (top/vm_stat/m1ddc/gh/kitten) skip-on-missing; env-gated live
    # tests self-skip. The oryx
    # tests listen on loopback (httptest) -- fine while darwin builds run
    # sandbox-off; if nix sandboxing is ever enabled here, this derivation
    # needs __darwinAllowLocalNetworking = true.
    doCheck = true;
  };

  # touchd: separate module + derivation so the TCC-granted binary stays small
  # and rarely rebuilt. When wired, pass a package set from a
  # frozen nixpkgs flake input via cfg.touchdPkgs (nixpkgs-qemu /
  # nixpkgs-claude precedent in flake.nix) so dep bumps on the main input
  # never touch this build. go-hid is cgo (IOKit); the apple SDK in stdenv
  # covers it.
  khudson-touchd = cfg.touchdPkgs.buildGoModule {
    pname = "khudson-touchd";
    version = "0.1.0";
    src = ../touchd;
    vendorHash = "sha256-UR7Ojpb3zNJS2UKgBFFJLJt24s9yEBOa6Y56xbc6RuQ=";
    # touchd's test suite is hermetic (cgo via the stdenv clang), so it runs
    # in the build like khudson's.
    doCheck = true;
    # go names the binary after the import path tail ("touchd")
    postInstall = ''
      mv $out/bin/touchd $out/bin/khudson-touchd
    '';
  };

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
      lib.generators.toPlist { escape = true; } {
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
    );

  agentCommands = {
    # Input: owns the Input Monitoring TCC grant, opens the digitizer HID,
    # asserts device mode, emits contact frames on touch.sock and Moonlander
    # key events on keys.sock. -daemon is LOAD-BEARING: the bare binary is
    # spike mode, which dies one-shot on the gestures-driver digitizer seize
    # and crash-loops under KeepAlive.
    touchd = ''/bin/wait4path "${touchdInstall}" && exec "${touchdInstall}" -daemon'';

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

    # The scrape substrate: a windowless regular-instance kitty hosting the
    # scraped-TUI windows (minimized at launch, then RC-hidden). Regular
    # instance because quick-access/panel instances never paint invisible
    # windows; --config NONE because nothing here is user-visible.
    # macos_hide_from_tasks is safe here -- this instance never fullscreens.
    # The stale-socket rm guards KeepAlive relaunches: a leftover socket
    # file would shadow the new bind (an exiting instance also unlinks a
    # shared path, so no other agent may reuse this socket path).
    substrate = ''/bin/wait4path "${pkgs.kitty}/bin/kitty" && rm -f "${appSupport}/kitty.sock" && exec "${pkgs.kitty}/bin/kitty" --config NONE -o allow_remote_control=socket-only -o macos_hide_from_tasks=yes --listen-on "unix:${appSupport}/kitty.sock" --title khudson-substrate --start-as hidden'';

    # Headless bus: recognizer, scheduler, RC client, widget supervision.
    # The Accessibility TCC client (dockmirror's direct AX walk plus the
    # `ax unminimize` row verb), so it execs the fixed install like the
    # hud agent above.
    bus = ''/bin/wait4path "${khudsonInstall}" && exec "${khudsonInstall}" bus -config "${config.xdg.configHome}/khudson/edge.cue"'';
  };
in
{
  options.universe.home.khudson = {
    enable = lib.mkEnableOption "khudson Edge HUD";

    # touchd is signed with a persistent identity from the login
    # keychain, NOT ad-hoc (`--sign -`), because ad-hoc = per-build cdhash =
    # every rebuild silently kills the Input Monitoring grant and KeepAlive
    # relaunches a touch-dead daemon.
    #
    # One-time per-machine bootstrap (imperative, accepted cost -- nix cannot
    # mint login-keychain identities):
    #   1. Keychain Access > Certificate Assistant > Create a Certificate...
    #      Name: khudson-touchd, Identity Type: Self-Signed Root,
    #      Certificate Type: Code Signing; store in the login keychain.
    #      (No supported `security` one-liner creates a code-signing identity;
    #      the GUI assistant is the documented path.)
    #   2. If codesign later reports the cert untrusted, export it as .cer and:
    #        security add-trusted-cert -p codeSign \
    #          -k "$HOME/Library/Keychains/login.keychain-db" khudson-touchd.cer
    #   3. Verify it resolves:
    #        security find-identity -p codesigning -v | grep khudson-touchd
    signingIdentity = lib.mkOption {
      type = lib.types.str;
      default = "khudson-touchd";
      description = "Login-keychain code-signing identity used for the out-of-store khudson and touchd installs.";
    };

    touchdPkgs = lib.mkOption {
      type = lib.types.pkgs;
      default = pkgs;
      defaultText = lib.literalExpression "pkgs";
      description = "Package set for the touchd build; point at a frozen nixpkgs input when wiring.";
    };

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

    # Fold the hand-applied main-kitty RC integration into the daily kitty
    # config (main-kitty-integration.md). Off by default even when the module
    # is enabled: it mutates programs.kitty (a shared, always-on config) and
    # needs the one-time hand-created rc-password.conf + a manual daily-kitty
    # relaunch, so it is opt-in rather than riding cfg.enable.
    mainKittyIntegration.enable = lib.mkEnableOption "declarative main-kitty RC socket + socket-only hardening for the khudson bus (needs a hand-created rc-password.conf; see main-kitty-integration.md)";
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
    home.packages = [ khudson ];

    # hud kitty instance config; khudson-scoped so the daily kitty config is
    # untouched. The main-kitty side (fixed RC socket, socket-only, password)
    # is folded into programs.kitty under cfg.mainKittyIntegration.enable
    # below (RC password stays hand-applied: it is a secret). The theme is
    # included from the same kitty-themes package the daily kitty uses, so
    # the two instances cannot drift apart again.
    xdg.configFile."khudson/hud-kitty.conf".source = pkgs.replaceVars ./hud-kitty.conf {
      everforestTheme = "${pkgs.kitty-themes}/share/kitty-themes/themes/everforest_dark_soft.conf";
    };
    xdg.configFile."khudson/edge.cue".source = edgeCUE;

    # Install the named launchers and ship each agent plist (see the agentsDir
    # comment for why this bypasses launchd.agents). Launchers reinstall
    # unconditionally -- content may embed new store paths (kitty) while the
    # plist, which only names the stable launcher path, stays byte-identical;
    # the running agents pick the new content up at the khudsonRestart
    # kickstarts. Plists bootout/reinstall/bootstrap only on change, plus a
    # bootstrap-if-unloaded repair leg so a hand-booted-out agent comes back.
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
          ${lib.concatStrings (
            lib.mapAttrsToList (name: cmd: ''
              run install -m 755 "${mkLauncher name cmd}" "${agentsDir}/khudson-${name}"
              if ! /usr/bin/cmp -s "${mkAgentPlist name}" "${config.home.homeDirectory}/Library/LaunchAgents/${agentLabel name}.plist"; then
                run /bin/launchctl bootout "gui/$khudsonAgentsUid/${agentLabel name}" 2>/dev/null || true
                run install -m 444 "${mkAgentPlist name}" "${config.home.homeDirectory}/Library/LaunchAgents/${agentLabel name}.plist"
                run /bin/launchctl bootstrap "gui/$khudsonAgentsUid" "${config.home.homeDirectory}/Library/LaunchAgents/${agentLabel name}.plist" || true
              elif ! /bin/launchctl print "gui/$khudsonAgentsUid/${agentLabel name}" > /dev/null 2>&1; then
                run /bin/launchctl bootstrap "gui/$khudsonAgentsUid" "${config.home.homeDirectory}/Library/LaunchAgents/${agentLabel name}.plist" || true
              fi
            '') agentCommands
          )}
        '';

    # --- ordered activation pipeline, DAG-encoded ---
    # config flip (linkGeneration, implicit)
    #   -> khudsonRuntimeDirs
    #   -> khudsonBinInstall + khudsonTouchdInstall
    #                             (explicitly before setupLaunchAgents, M1d)
    #   -> setupLaunchAgents      (home-manager: retires the old
    #                              org.nix-community.home.* agents)
    #   -> khudsonAgents          (module-owned: launchers + plists + bootstrap)
    #   -> khudsonRestart         (substrate -> bus -> hud-launcher, liveness-gated)
    # Accepted cost: any switch touching khudson restarts the whole HUD,
    # including keepAlive scraped panes.

    home.activation.khudsonRuntimeDirs = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      # -m 700 every time: BSD install -d chmods a pre-existing directory to
      # its default 0755, so omitting the mode RE-OPENS the state root (and
      # its sockets) to world-read on every switch
      run install -d -m 700 "${appSupport}" "${appSupport}/bin" "${appSupport}/claude" "${appSupport}/log" "${btopCfgDir}/btop"
      # re-seed every switch: btop rewrites its config on exit, so activation
      # restores the declared baseline rather than letting it drift
      run install -m 644 "${btopConf}" "${btopCfgDir}/btop/btop.conf"
    '';

    # khudson gets the same copy-out-and-sign as touchd below (see that block
    # for the staging discipline and the no-hardened-runtime rationale, which
    # applies verbatim: khudson links nix-store dylibs too). No .updated
    # marker: khudsonRestart kickstarts bus + hud unconditionally. The script
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
            ${lib.escapeShellArg cfg.signingIdentity}
        '';

    # Copy touchd out of the store to a fixed path and sign it
    # with the persistent identity when the store source changed OR the
    # installed signature no longer verifies (install-script.nix; the verify
    # runs on every activation). The .touchd-updated marker tells
    # khudsonRestart whether a kickstart is owed, so the script only touches
    # it on the reinstall branch.
    #
    # stamp = store path + signing recipe: a changed codesign invocation
    # must re-sign even when the store build is unchanged.
    #
    # The script stages + signs BESIDE the granted binary and replaces
    # atomically only after codesign succeeds (M1c): signing the install
    # path in place means a codesign failure (identity missing on a fresh
    # host, expired cert) leaves an unsigned copy where the TCC-granted
    # binary was, and KeepAlive relaunches it with the grant dead.
    #
    # NO --options runtime: hardened runtime enforces library
    # validation, and touchd links a nix-store dylib (libresolv)
    # whose ad-hoc store signature fails it ("different Team IDs") --
    # the daemon died in dyld before main.
    # TCC grants key on the signing identity, not hardened runtime.
    home.activation.khudsonTouchdInstall =
      lib.hm.dag.entryBetween [ "setupLaunchAgents" ] [ "khudsonRuntimeDirs" ]
        ''
          run ${binInstallScript}/bin/khudson-bin-install \
            "${khudson-touchd}/bin/khudson-touchd" \
            "${touchdInstall}" \
            "${appSupport}/bin/.khudson-touchd.store-path" \
            "${khudson-touchd} ${touchdSignRecipe}" \
            ${lib.escapeShellArg cfg.signingIdentity} \
            "${appSupport}/.touchd-updated"
        '';

    # One ordered restart pipeline, each step gated on liveness of
    # the previous (socket presence as the proxy). Skeleton: gates warn rather
    # than abort activation, and dock-adopt is a stub verb until khudson ctl
    # lands. `@ ls` topology snapshots happen inside the bus only after its
    # kitty gate passed, so the bus never snapshots a dying instance.
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
      khudsonUid=$(id -u)

      # 1. touchd, only if the install step replaced the binary. Its TCC
      #    grant survives the restart because path + signing identity are
      #    stable (M1).
      if [ -e "${appSupport}/.touchd-updated" ]; then
        run /bin/launchctl kickstart -k "gui/$khudsonUid/${agentLabel "touchd"}" || true
        run rm -f "${appSupport}/.touchd-updated"
      fi

      # 2. scrape substrate. The agent clears its own stale socket pre-exec;
      #    the wait gates the bus on a live substrate to adopt against.
      run /bin/launchctl kickstart -k "gui/$khudsonUid/${agentLabel "substrate"}" || true
      khudsonWaitSock "${appSupport}/kitty.sock" || true

      # 3. bus, only after the substrate is up.
      run /bin/launchctl kickstart -k "gui/$khudsonUid/${agentLabel "bus"}" || true
      khudsonWaitSock "${appSupport}/khudson.sock" || true

      # 4. hud-launcher. NO socket wait: the launcher only creates the HUD
      #    (and its kitty-panel.sock) while the Edge is connected, so a wait
      #    here would stall every switch on an unplugged display. The dock
      #    reconnects to the bus whenever it does come up.
      run /bin/launchctl kickstart -k "gui/$khudsonUid/${agentLabel "hud"}" || true

      # 5. dock-adopt: dock re-handshakes with the bus (reports grid +
      #    per-slot view state). Stub until `khudson ctl adopt`
      #    exists; the dock itself was already restarted with its kitty.
      run "${khudson}/bin/khudson" ctl adopt 2>/dev/null || true
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
    # and the quick-access RC-off override are new keys. The RC password
    # (remote_control_password) stays hand-applied: a literal in nix lands
    # world-readable in the store, so rc-password.conf is a user-owned 0600
    # include (see the doc). Caveat, unchanged: listen_on binds only at kitty
    # startup, so ONE manual daily-kitty relaunch is owed after first enabling.
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
          # RC only via the fixed socket below; tty RC is refused, so programs
          # inside windows cannot drive kitty without the password.
          allow_remote_control = lib.mkForce "socket-only";
        };
        # Fixed, non-pid socket, delivered as a LAUNCH option, not settings:
        # config-form unix listen_on gets "-<PID>" appended (kitty
        # expand_listen_on, main.py:409-410 -- same semantics the panel socket
        # comment above documents), so only CLI --listen-on binds the exact
        # path the bus contract promises.
        # The launcher shlex-parses macos-launch-services-cmdline and
        # home-manager joins this list with bare spaces, so the space in
        # "Application Support" needs the embedded quotes. Caveat: the file is
        # read only for Launch-Services launches -- a shell-spawned kitty
        # binds no socket (acceptable: the daily kitty is LS-launched).
        darwinLaunchOptions = [
          "--listen-on"
          "'unix:${appSupport}/main-kitty.sock'"
        ];
        # A literal RC password in nix is world-readable in the store, so the
        # real remote_control_password line lives in a hand-created 0600
        # include; a missing include is ignored with only a warning, so first
        # activation does not break kitty startup (main-kitty-integration.md).
        extraConfig = ''
          include rc-password.conf
        '';
        # The quick-access terminal is a second kitty process on the same
        # kitty.conf; RC off means it never binds listen_on, so it cannot
        # squat or shadow the fixed socket and exposes no second RC surface.
        quickAccessTerminalConfig.kitty_override = "allow_remote_control=no";
      };
    })
  ];
}
