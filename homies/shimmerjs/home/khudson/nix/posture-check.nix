# Build-time posture pins for khudson's RC and state-root hardening: pure
# string assertions on the RENDERED artifacts of the host config (hmCfg =
# the host user's home-manager config), no runtime. Four legs, each anchored
# on a positive marker first so nothing passes vacuously. The posture is
# socket-only everywhere, passwordless by design: kitty never consults
# remote_control_password for socket peers, so any password line is inert
# posture-theater and legs (a)/(c) now REJECT it outright.
#   (a) hud-kitty.conf     socket-only RC, no password line
#   (b) owner launchers    bus owns the renderer; Reno has its own owner
#   (c) daily kitty        kitty.conf socket-only RC, no password include,
#                          no password line; the bundle's LSEnvironment keeps
#                          KITTY_LAUNCHED_BY_LAUNCH_SERVICES=1 and
#                          macos-launch-services-cmdline parses to one
#                          --listen-on unix:<state root>/main-kitty.sock
#   (d) state root         khudsonRuntimeDirs re-asserts -m 700 on the
#                          ".../Library/Application Support/khudson" root
#                          every activation (BSD install -d would silently
#                          re-open it to 0755 otherwise)
{ pkgs, hmCfg }:
let
  hudKitty = hmCfg.xdg.configFile."khudson/hud-kitty.conf".source;
  # Leg (c) asserts the main-kitty posture, which only EXISTS when
  # mainKittyIntegration is on (off-by-default even with khudson enabled);
  # gating here keeps the check truthful to the module contract instead of
  # false-failing valid configs. The attr access is also
  # guarded: without programs.kitty there is no kitty.conf entry at all.
  mainKittyOn = hmCfg.universe.home.khudson.mainKittyIntegration.enable or false;
  dailyKittyEntry = hmCfg.xdg.configFile."kitty/kitty.conf" or null;
  dailyKitty =
    if !mainKittyOn || dailyKittyEntry == null then
      null
    else if (dailyKittyEntry.source or null) != null then
      dailyKittyEntry.source
    else
      pkgs.writeText "daily-kitty.conf" dailyKittyEntry.text;
  dailyKittyApp =
    if dailyKitty == null then "" else "${hmCfg.programs.kitty.package}/Applications/kitty.app";
  lsCmdline =
    if dailyKitty == null then "" else hmCfg.xdg.configFile."kitty/macos-launch-services-cmdline".source;
  mainKittyListen = "unix:${hmCfg.home.homeDirectory}/Library/Application Support/khudson/main-kitty.sock";
  # kitty reads macos-launch-services-cmdline only when LaunchServices hands
  # it KITTY_LAUNCHED_BY_LAUNCH_SERVICES=1 from the bundle's LSEnvironment,
  # which modules/darwin/kitty-overlay.nix rewrites.
  lsLaunchCheck = pkgs.writeText "main-kitty-ls-launch.py" ''
    import plistlib
    import shlex
    import sys

    app, cmdline, want = sys.argv[1:]
    with open(app + "/Contents/Info.plist", "rb") as f:
        env = plistlib.load(f).get("LSEnvironment", {})
    if env.get("KITTY_LAUNCHED_BY_LAUNCH_SERVICES") != "1":
        sys.exit(f"POSTURE FAIL: {app} LSEnvironment lacks KITTY_LAUNCHED_BY_LAUNCH_SERVICES=1 (got {env}); an LS-launched kitty skips macos-launch-services-cmdline and never binds main-kitty.sock")
    with open(cmdline) as f:
        argv = shlex.split(f.read())
    if argv != ["--listen-on", want]:
        sys.exit(f"POSTURE FAIL: macos-launch-services-cmdline parses to {argv}, want exactly one --listen-on {want!r}")
  '';
  couchOn = hmCfg.universe.home.khudson.couchMode.enable or false;
  couchKitty = if couchOn then hmCfg.programs.kitty.quickAccessTerminalConfig.kitty_conf else "";
in
pkgs.runCommand "khudson-posture-check"
  {
    inherit
      hudKitty
      dailyKitty
      dailyKittyApp
      lsCmdline
      mainKittyListen
      couchKitty
      ;
    # The activation scripts as rendered strings; their string context
    # carries the launcher store paths, so the files exist in this build.
    agentsData = hmCfg.home.activation.khudsonAgents.data;
    magicAgentsData = hmCfg.home.activation.magicbusAgents.data;
    magicInstallData = hmCfg.home.activation.khudsonTouchdInstall.data;
    binInstallData = hmCfg.home.activation.khudsonBinInstall.data;
    runtimeDirsData = hmCfg.home.activation.khudsonRuntimeDirs.data;
    passAsFile = [
      "agentsData"
      "magicAgentsData"
      "magicInstallData"
      "binInstallData"
      "runtimeDirsData"
    ];
  }
  ''
    fail() { echo "POSTURE FAIL: $*" >&2; exit 1; }

    ${pkgs.bash}/bin/bash ${./agent-lifecycle-test.sh} ${./agent-lifecycle.sh}

    for script in "$agentsDataPath" "$magicAgentsDataPath" "$magicInstallDataPath"; do
      ${pkgs.bash}/bin/bash -n "$script" || fail "invalid activation shell"
    done
    grep -Fq 'khudsonRetireOwner "org.magicbus.daemon" || exit 1' "$magicAgentsDataPath" \
      || fail "hardware owner retirement is not checked"
    if grep -Eq 'kickstart|bootstrap .*\|\| true' "$magicAgentsDataPath"; then
      fail "hardware replacement force-restarts or ignores failure"
    fi

    echo "(a) hud-kitty.conf: socket-only, no password"
    grep -Fq 'allow_remote_control socket-only' "$hudKitty" \
      || fail "hud-kitty.conf lost 'allow_remote_control socket-only'"
    if grep -q 'remote_control_password' "$hudKitty"; then
      fail "hud-kitty.conf grew a remote_control_password (inert on socket peers; the posture is passwordless socket-only)"
    fi

    echo "(b) bus owns the on-demand renderer"
    line=$(grep -E 'install -m 755 "[^"]+" "[^"]+/agents/khudson-bus"' "$agentsDataPath") \
      || fail "missing bus launcher"
    launcher=$(printf '%s\n' "$line" | head -n1 | sed -E 's/.*install -m 755 "([^"]+)".*/\1/')
    grep -Fq -- '-renderer-kitty' "$launcher" || fail "bus does not own renderer"
    if grep -E 'install -m 755 "[^"]+" "[^"]+/agents/khudson-substrate"' "$agentsDataPath"; then
      fail "always-on substrate agent remains"
    fi

    line=$(grep -E 'install -m 755 "[^"]+" "[^"]+/agents/khudson-reno"' "$agentsDataPath") \
      || fail "missing Reno owner launcher"
    launcher=$(printf '%s\n' "$line" | head -n1 | sed -E 's/.*install -m 755 "([^"]+)".*/\1/')
    grep -Eq '/bin/renoman" serve' "$launcher" || fail "Reno launcher does not run shared owner"
    grep -Fq 'khudsonRetireOwner "org.khudson.bus" || exit 1' "$agentsDataPath" || fail "old bus ownership is not retired"
    grep -Fq 'khudsonRetireOwner "org.khudson.reno" || exit 1' "$agentsDataPath" || fail "old Reno ownership is not retired"
    package=$(sed -n 's@.*"\(/nix/store/[^" ]*\)/bin/renoman".*@\1@p' "$binInstallDataPath" | head -n1)
    for program in khcomponent renoman claudeman logiretch khresources kuiboard; do
      test -x "$package/bin/$program" || fail "$program is not packaged"
    done

    echo "(c) daily kitty: socket-only, passwordless, LS launch binds main-kitty.sock"
    if [ -n "$dailyKitty" ]; then
      grep -Fq 'allow_remote_control socket-only' "$dailyKitty" \
        || fail "daily kitty.conf lost 'allow_remote_control socket-only'"
      if grep -q 'rc-password.conf\|remote_control_password' "$dailyKitty"; then
        fail "daily kitty.conf regrew password machinery (inert on socket peers; retired 2026-07-10)"
      fi
      ${pkgs.python3.interpreter} ${lsLaunchCheck} "$dailyKittyApp" "$lsCmdline" "$mainKittyListen" || exit 1
    else
      echo "    (skipped: mainKittyIntegration off -- posture has no daily-kitty leg)"
    fi

    echo "(d) state root installed 0700"
    grep -Eq 'install -d -m 700 "[^"]+/Library/Application Support/khudson"' "$runtimeDirsDataPath" \
      || fail "khudsonRuntimeDirs no longer applies install -d -m 700 to the khudson state root"

    if [ -n "$couchKitty" ]; then
      grep -Fxq 'allow_remote_control no' "$couchKitty" || fail "couch RC must remain disabled"
      session=$(sed -n 's/^startup_session //p' "$couchKitty")
      test -f "$session" || fail "missing couch startup session"
      for program in claudeman kuiboard logiretch khresources; do
        executable=$(grep "/bin/$program" "$session" | awk '{print $NF}')
        test -x "$executable" || fail "couch component $program is not packaged"
      done
      if grep -Eq 'khudson (dock|bus)|grab.keyboard' "$session"; then
        fail "couch session must launch independent component programs"
      fi
    fi

    touch $out
  ''
