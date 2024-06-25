# Config for Rectangle, window management for macOS.
{ pkgs, ... }:
{
  home.file."Library/Preferences/com.knollsoft.Rectangle.plist".source = ./com.knollsoft.Rectangle.plist;
  home.packages = with pkgs; [
    rectangle
  ];
}
