# Config module for Rectangle, window management for macOS.
# TODO: Make it usable for Linux OS as well
{ pkgs, config, lib, ... }:
let
  cfg = config.universe.home.rectangle;
in
with lib;
{
  options = {
    universe.home.rectangle = {
      enable = mkEnableOption "Rectangle";

      configFile = mkOption {
        type = types.attrs;
        description = ''
          Attributes for home-manager File object that defines a Rectangle 
          configuration file.
        '';
      };
    };
  };

  config.home.packages =
    mkIf cfg.enable
      [ pkgs.rectangle ];
  config.home.file."Library/Preferences/com.knollsoft.Rectangle.plist" =
    mkIf (cfg.configFile != { })
      cfg.configFile;
}
