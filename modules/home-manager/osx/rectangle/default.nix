{ ... }:
let
  cfgPath = "Library/Preferences/com.knollsoft.Rectangle.plist";
in
{
  home.file."${cfgPath}".source = ./com.knollsoft.Rectangle.plist;
}
