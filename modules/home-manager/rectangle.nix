# Config module for Rectangle, window management for macOS.
# TODO: Make it usable for Linux OS as well
{
  pkgs,
  config,
  lib,
  ...
}:
let
  cfg = config.universe.home.rectangle;
in
with lib;
{
  options = {
    universe.home.rectangle = {
      enable = mkEnableOption "Rectangle";

      configFile = mkOption {
        type = types.path;
        description = "Path to a Rectangle plist configuration file.";
      };

      signingIdentity = mkOption {
        type = types.str;
        default = "-";
        description = ''
          Login-keychain code-signing identity to re-sign the installed
          Rectangle.app with. nixpkgs builds Rectangle from source and
          ad-hoc signs it, so every rebuild ships a new code identity and
          macOS drops the Accessibility grant (TCC keys grants on the
          signing requirement; an ad-hoc requirement is that binary's
          literal cdhash). Re-signing with a stable author cert anchors
          the requirement to the certificate, so the grant survives
          version and toolchain bumps. "-" (ad-hoc) keeps stock behavior.
        '';
      };
    };
  };

  config = mkIf cfg.enable {
    home.packages = [ pkgs.rectangle ];

    # Runs before rectangleConfig's relaunch so a fresh copy is signed
    # before it launches. --deep covers the bundled Sparkle-stub and
    # MASShortcut frameworks; the bundle is self-contained, so no
    # library-validation exposure.
    home.activation.signRectangle = lib.hm.dag.entryAfter [ "linkGeneration" ] (
      lib.optionalString (pkgs.stdenv.hostPlatform.isDarwin && cfg.signingIdentity != "-") ''
        run /usr/bin/codesign --force --deep --sign ${lib.escapeShellArg cfg.signingIdentity} \
          "$HOME/Applications/Home Manager Apps/Rectangle.app"
      ''
    );

    # Rectangle owns its prefs domain while running; a bare `defaults import`
    # gets clobbered when the live app next syncs. Quit it, import, relaunch.
    home.activation.rectangleConfig = lib.hm.dag.entryAfter [ "writeBoundary" "signRectangle" ] ''
      wasRunning=0
      if /usr/bin/pgrep -x Rectangle >/dev/null 2>&1; then
        wasRunning=1
        $DRY_RUN_CMD /usr/bin/osascript -e 'quit app "Rectangle"' >/dev/null 2>&1 || true
        for _ in 1 2 3 4 5 6; do
          /usr/bin/pgrep -x Rectangle >/dev/null 2>&1 || break
          /bin/sleep 0.5
        done
        if /usr/bin/pgrep -x Rectangle >/dev/null 2>&1; then
          $DRY_RUN_CMD /usr/bin/killall Rectangle >/dev/null 2>&1 || true
          /bin/sleep 0.5
        fi
      fi
      $DRY_RUN_CMD defaults import com.knollsoft.Rectangle "${cfg.configFile}"
      if [ "$wasRunning" = 1 ]; then
        $DRY_RUN_CMD /usr/bin/open -a Rectangle >/dev/null 2>&1 || true
      fi
    '';
  };
}
