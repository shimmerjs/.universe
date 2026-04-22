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

    home.activation.rectangleConfig = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      $DRY_RUN_CMD defaults import com.knollsoft.Rectangle "${cfg.configFile}"
    '';
  };
}
