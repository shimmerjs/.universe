# Build-time posture pins for khudson's RC and state-root hardening: pure
# string assertions on the RENDERED artifacts of the host config (hmCfg =
# the host user's home-manager config), no runtime. Four legs, each anchored
# on a positive marker first so nothing passes vacuously:
#   (a) hud-kitty.conf     socket-only RC, and NO password (the daily kitty
#                          is the only password-bearing socket)
#   (b) substrate argv     the launcher's kitty line carries
#                          -o allow_remote_control=socket-only
#   (c) daily kitty.conf   include rc-password.conf + socket-only RC
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
in
pkgs.runCommand "khudson-posture-check"
  {
    inherit hudKitty dailyKitty;
    # The activation scripts as rendered strings; their string context
    # carries the launcher store paths, so the files exist in this build.
    agentsData = hmCfg.home.activation.khudsonAgents.data;
    runtimeDirsData = hmCfg.home.activation.khudsonRuntimeDirs.data;
    passAsFile = [
      "agentsData"
      "runtimeDirsData"
    ];
  }
  ''
    fail() { echo "POSTURE FAIL: $*" >&2; exit 1; }

    echo "(a) hud-kitty.conf: socket-only, no password"
    grep -Fq 'allow_remote_control socket-only' "$hudKitty" \
      || fail "hud-kitty.conf lost 'allow_remote_control socket-only'"
    if grep -q 'remote_control_password' "$hudKitty"; then
      fail "hud-kitty.conf grew a remote_control_password (the daily kitty is the ONLY password-bearing socket)"
    fi

    echo "(b) substrate agent argv: socket-only RC"
    line=$(grep -E 'install -m 755 "[^"]+" "[^"]+/agents/khudson-substrate"' "$agentsDataPath") \
      || fail "khudsonAgents activation has no khudson-substrate launcher install line"
    launcher=$(printf '%s\n' "$line" | head -n1 | sed -E 's/.*install -m 755 "([^"]+)".*/\1/')
    [ -r "$launcher" ] || fail "substrate launcher $launcher is not readable"
    grep -Fq -- '-o allow_remote_control=socket-only' "$launcher" \
      || fail "substrate kitty argv lost '-o allow_remote_control=socket-only'"

    echo "(c) daily kitty.conf: password include + socket-only"
    if [ -n "$dailyKitty" ]; then
    grep -Fq 'include rc-password.conf' "$dailyKitty" \
      || fail "daily kitty.conf lost 'include rc-password.conf'"
    grep -Fq 'allow_remote_control socket-only' "$dailyKitty" \
      || fail "daily kitty.conf lost 'allow_remote_control socket-only'"
    else
      echo "    (skipped: mainKittyIntegration off -- posture has no daily-kitty leg)"
    fi

    echo "(d) state root installed 0700"
    grep -Eq 'install -d -m 700 "[^"]+/Library/Application Support/khudson"' "$runtimeDirsDataPath" \
      || fail "khudsonRuntimeDirs no longer applies install -d -m 700 to the khudson state root"

    touch $out
  ''
