# magicbus: the modular HID daemon (khudson-touchd) as its own home-manager
# module, so a keyboard-only host can run the Moonlander source without the
# HUD stack (magicbus-design.md). Owns everything touchd-scoped: the
# derivation, the out-of-store signed install, the launchd agent, the
# rendered -config JSON, and the kickstart markers. The binary name, install
# path, launchd label, and signing identity are FROZEN -- the Input
# Monitoring TCC grant keys on them -- which is also why keyboard-only hosts
# keep the khudson naming (accepted debt, design open question 1).
{
  pkgs,
  lib,
  config,
  ...
}:
let
  cfg = config.universe.home.magicbus;

  # Same state root khudson uses: the socket paths (touch.sock, keys.sock)
  # are a frozen contract with the bus, docks, and kuiboard.
  appSupport = "${config.home.homeDirectory}/Library/Application Support/khudson";

  # Fixed out-of-store install path: the Input Monitoring grant
  # keys on the binary's path + signature, so the granted binary must never
  # move with the store generation.
  touchdInstall = "${appSupport}/bin/khudson-touchd";

  # Joins the install stamp beside the store path; bump the version whenever
  # the codesign invocation in khudsonTouchdInstall changes so existing
  # installs get re-signed on the next switch.
  touchdSignRecipe = "sign-v2:${cfg.signingIdentity}";

  # The verify+reinstall logic shared with khudson's install, so the
  # khudson-bininstall flake check (install-check.nix) exercises the exact
  # script activation runs.
  binInstallScript = import ./install-script.nix { inherit pkgs; };

  # touchd: its own derivation so the TCC-granted binary stays small
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
    # touchd's test suite is hermetic (cgo via the stdenv clang, HID behind
    # an enumerate seam), so it runs in the build like khudson's.
    doCheck = true;
    # go names the binary after the import path tail ("touchd")
    postInstall = ''
      mv $out/bin/touchd $out/bin/khudson-touchd
    '';
  };

  # Per-host module config: authored in CUE, vetted against the schema, and
  # exported to the plain JSON the daemon's -config flag reads (stdlib
  # encoding/json only -- cuelang.org/go must NOT enter the TCC binary, its
  # init zeroes stdlib log flags). The daemon fails fast on a bad file, so a
  # keyboard-only host can never silently reinstate a perpetual Edge poll.
  # logiretch device settings, nulls dropped so an unset field is absent from
  # the exported JSON and the daemon leaves that capability alone. Nested
  # objects (smartShift) drop their own null subfields; emitted as JSON, which
  # is valid CUE data, so `cue vet` still checks it against #Logiretch.
  dropNull = lib.filterAttrs (_: v: v != null);
  logiretchSettings =
    let
      lg = cfg.logiretch;
    in
    dropNull {
      inherit (lg)
        dpi
        hiresWheel
        thumbwheel
        haptic
        takeoverReset
        batteryPollSec
        buttons
        ;
      smartShift = if lg.smartShift == null then null else dropNull { inherit (lg.smartShift) mode threshold torque; };
    };
  magicbusConfigValues = pkgs.writeText "magicbus-config-values.cue" ''
    package magicbus

    config: modules: {
      edge:       ${lib.boolToString cfg.modules.edge}
      moonlander: ${lib.boolToString cfg.modules.moonlander}
      logiretch:  ${lib.boolToString cfg.modules.logiretch}
    }
    ${lib.optionalString (logiretchSettings != { }) "config: logiretch: ${builtins.toJSON logiretchSettings}"}
  '';
  touchdConfig =
    pkgs.runCommand "khudson-touchd-config.json"
      {
        nativeBuildInputs = [ pkgs.cue ];
      }
      ''
        cue vet -c ${./magicbus-config.cue} ${magicbusConfigValues}
        cue export ${./magicbus-config.cue} ${magicbusConfigValues} -e config --out json > $out
      '';

  # Reinstall marker (touched by install-script.nix's reinstall branch only)
  # and the SEPARATE config-hash marker: a config edit owes a kickstart but
  # must never force a full reinstall+re-sign, so it does not fold into the
  # install stamp.
  touchdUpdatedMarker = "${appSupport}/.touchd-updated";
  touchdConfigMarker = "${appSupport}/.touchd-config-updated";
  touchdConfigStamp = "${appSupport}/bin/.khudson-touchd.config-path";

  # Module-shipped plist + named launcher, same mechanics as khudson's
  # agents (see the agentsDir comment in module.nix for why this bypasses
  # home-manager's launchd.agents): ProgramArguments[0] is the stable
  # out-of-store launcher path (BTM keys the Login Item identity on it), the
  # launcher content embeds the store paths and is reinstalled
  # unconditionally, the plist only changes when the label machinery does.
  agentsDir = "${appSupport}/agents";
  agentLabel = "org.khudson.touchd";
  launcherPath = "${agentsDir}/khudson-touchd";
  plistPath = "${config.home.homeDirectory}/Library/LaunchAgents/${agentLabel}.plist";
  # -daemon is LOAD-BEARING: the bare binary is spike mode, which dies
  # one-shot on the gestures-driver digitizer seize and crash-loops under
  # KeepAlive. wait4path parks a fresh boot (or fresh host) until
  # khudsonTouchdInstall has run once.
  touchdLauncher = pkgs.writeText "khudson-touchd-launcher" ''
    #!/bin/sh
    /bin/wait4path /nix/store
    /bin/wait4path "${touchdInstall}" && exec "${touchdInstall}" -daemon -config "${touchdConfig}" -logi-socket "${appSupport}/logiretch.sock"
  '';
  touchdPlist = pkgs.writeText "${agentLabel}.plist" (
    lib.generators.toPlist { escape = true; } {
      Label = agentLabel;
      ProgramArguments = [ launcherPath ];
      # launchd's default PATH is /usr/bin:/bin:/usr/sbin:/sbin; keep the
      # nix dirs so the daemon matches the other khudson agents (and any
      # future module exec needs resolve).
      EnvironmentVariables.PATH =
        "/etc/profiles/per-user/${config.home.username}/bin:/run/current-system/sw/bin:/usr/bin:/bin:/usr/sbin:/sbin";
      RunAtLoad = true;
      KeepAlive = true;
      ProcessType = "Interactive";
      StandardOutPath = "${appSupport}/log/touchd.log";
      StandardErrorPath = "${appSupport}/log/touchd.log";
    }
  );
in
{
  options.universe.home.magicbus = {
    enable = lib.mkEnableOption "magicbus HID daemon (khudson-touchd)";

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
    #
    # SINGLE SOURCE for the identity string: khudson signs its Accessibility
    # client with this same option (module.nix references it) -- never
    # duplicate or rename it, and never delete the cert while any host runs
    # khudson.
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

    # A disabled module is literally zero in the daemon: no goroutine, no
    # socket bind, no arrival-scanner membership.
    modules = {
      edge = lib.mkEnableOption "Corsair Xeneon Edge touch source (touch.sock)";
      moonlander = lib.mkEnableOption "ZSA Moonlander key-event source (keys.sock)";
      logiretch = lib.mkEnableOption "Logitech MX Master 4 source (logiretch.sock): battery + config-apply + Options+ divert-reset";
    };

    # logiretch device settings. All optional: an unset (null) field is
    # dropped from the exported config, and the module leaves that capability
    # alone (absent = do not touch the device). Meaningful only after Options+
    # is uninstalled -- with it installed both HID++ masters share the node and
    # Options+ re-diverts.
    logiretch =
      let
        nullInt = lib.mkOption {
          type = lib.types.nullOr lib.types.int;
          default = null;
        };
        nullBool = lib.mkOption {
          type = lib.types.nullOr lib.types.bool;
          default = null;
        };
      in
      {
        dpi = nullInt // {
          description = "Target DPI (0x2201); snapped to the device's supported step list.";
        };
        smartShift = lib.mkOption {
          type = lib.types.nullOr (
            lib.types.submodule {
              options = {
                mode = nullInt;
                threshold = nullInt;
                torque = nullInt;
              };
            }
          );
          default = null;
          description = "SmartShift (0x2111): wheel mode / auto-disengage threshold / tunable torque; each subfield optional.";
        };
        hiresWheel = nullBool // {
          description = "Hi-res wheel resolution (0x2121). EXPERIMENTAL on the MX4 (default untouched).";
        };
        thumbwheel = nullBool // {
          description = "Invert the thumbwheel scroll direction (0x2150).";
        };
        haptic = lib.mkOption {
          type = lib.types.nullOr (lib.types.ints.between 0 100);
          default = null;
          description = "Haptic feedback level 0-100 (0x19B0).";
        };
        buttons = lib.mkOption {
          type = lib.types.nullOr (
            lib.types.listOf (
              lib.types.submodule {
                options = {
                  cid = lib.mkOption { type = lib.types.ints.between 0 65535; };
                  remap = lib.mkOption { type = lib.types.ints.between 0 65535; };
                };
              }
            )
          );
          default = null;
          description = "0x1B04 remaps: each { cid; remap; } is applied (not cleared) on takeover.";
        };
        takeoverReset = nullBool // {
          description = "Clear Options+ 1B04 divert/rawXY residue on takeover (default true in the daemon).";
        };
        batteryPollSec = lib.mkOption {
          type = lib.types.nullOr (lib.types.ints.between 60 300);
          default = null;
          description = "Battery poll cadence in seconds (daemon default 120, clamped 60-300).";
        };
      };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.modules.edge || cfg.modules.moonlander || cfg.modules.logiretch;
        message = "universe.home.magicbus: enable at least one of modules.edge / modules.moonlander / modules.logiretch (the daemon exits nonzero on an empty module set).";
      }
    ];

    home.activation.magicbusRuntimeDirs = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      # -m 700 every time: BSD install -d chmods a pre-existing directory to
      # its default 0755, so omitting the mode RE-OPENS the state root (and
      # its sockets) to world-read on every switch
      run install -d -m 700 "${appSupport}" "${appSupport}/bin" "${appSupport}/log"
    '';

    # Copy touchd out of the store to a fixed path and sign it
    # with the persistent identity when the store source changed OR the
    # installed signature no longer verifies (install-script.nix; the verify
    # runs on every activation). The .touchd-updated marker tells
    # magicbusKickstart whether a kickstart is owed, so the script only touches
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
    #
    # Keeps its khudson-era name: khudson's khudsonAgents step orders after
    # it by name, and existing installs keep their stamp path.
    home.activation.khudsonTouchdInstall =
      lib.hm.dag.entryBetween [ "setupLaunchAgents" ] [ "magicbusRuntimeDirs" ]
        ''
          run ${binInstallScript}/bin/khudson-bin-install \
            "${khudson-touchd}/bin/khudson-touchd" \
            "${touchdInstall}" \
            "${appSupport}/bin/.khudson-touchd.store-path" \
            "${khudson-touchd} ${touchdSignRecipe}" \
            ${lib.escapeShellArg cfg.signingIdentity} \
            "${touchdUpdatedMarker}"
        '';

    # Launcher reinstalls unconditionally -- content embeds store paths (the
    # binary install path is stable, but -config moves with the rendered
    # JSON) while the plist, which only names the stable launcher path,
    # stays byte-identical. Plist bootout/reinstall/bootstrap only on
    # change, plus a bootstrap-if-unloaded repair leg so a hand-booted-out
    # agent comes back.
    home.activation.magicbusAgents =
      lib.hm.dag.entryBetween
        [ "magicbusKickstart" ]
        [
          "setupLaunchAgents"
          "khudsonTouchdInstall"
        ]
        ''
          run install -d -m 700 "${agentsDir}"
          magicbusAgentsUid=$(id -u)
          run install -m 755 "${touchdLauncher}" "${launcherPath}"
          if ! /usr/bin/cmp -s "${touchdPlist}" "${plistPath}"; then
            run /bin/launchctl bootout "gui/$magicbusAgentsUid/${agentLabel}" 2>/dev/null || true
            run install -m 444 "${touchdPlist}" "${plistPath}"
            run /bin/launchctl bootstrap "gui/$magicbusAgentsUid" "${plistPath}" || true
          elif ! /bin/launchctl print "gui/$magicbusAgentsUid/${agentLabel}" > /dev/null 2>&1; then
            run /bin/launchctl bootstrap "gui/$magicbusAgentsUid" "${plistPath}" || true
          fi
        '';

    # A config change owes a kickstart but NOT a reinstall+re-sign: the
    # install stamp only tracks binary + recipe, so the rendered config gets
    # its own hash marker, written before the kickstart leg reads it.
    home.activation.magicbusConfigStamp =
      lib.hm.dag.entryBetween [ "magicbusKickstart" ] [ "magicbusRuntimeDirs" ] ''
        if [ "$(cat "${touchdConfigStamp}" 2>/dev/null || true)" != "${touchdConfig}" ]; then
          run touch "${touchdConfigMarker}"
          [ -n "''${DRY_RUN:-}" ] || printf %s "${touchdConfig}" > "${touchdConfigStamp}"
        fi
      '';

    # Kickstart when EITHER marker says the running daemon is stale (new
    # binary or new config). The TCC grant survives the restart because
    # path + signing identity are stable (M1). Ordered before khudsonRestart
    # (when present) to keep the old touchd-first restart sequence.
    home.activation.magicbusKickstart =
      lib.hm.dag.entryBetween [ "khudsonRestart" ] [ "magicbusAgents" ] ''
        if [ -e "${touchdUpdatedMarker}" ] || [ -e "${touchdConfigMarker}" ]; then
          magicbusUid=$(id -u)
          run /bin/launchctl kickstart -k "gui/$magicbusUid/${agentLabel}" || true
          run rm -f "${touchdUpdatedMarker}" "${touchdConfigMarker}"
        fi
      '';
  };
}
