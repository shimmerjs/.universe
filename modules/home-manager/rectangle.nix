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
    };
  };

  config = mkIf cfg.enable {
    home.packages = [ pkgs.rectangle ];

    # Rectangle owns its prefs domain while running; a bare `defaults import`
    # gets clobbered when the live app next syncs. Quit it, import, relaunch.
    home.activation.rectangleConfig = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
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
