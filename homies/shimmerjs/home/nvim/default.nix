{
  pkgs,
  lib,
  inputs,
  ...
}:
let
  utils = inputs.nixCats.utils;
in
{
  imports = [ inputs.nixCats.homeModule ];
  config.nixCats = {
    enable = true;
    # This will add any plugins in inputs named "plugins-pluginName" to
    # pkgs.neovimPlugins
    # It will not apply to overall system, just nixCats.
    addOverlays = [
      (utils.standardPluginOverlay inputs)
    ];

    # Which package definition below to build the final nvim package from
    # packageNames = [ "mevim" ];

    luaPath = "./";
  };
  # programs.neovim = {
  #   enable = true;

  #   viAlias = true;
  #   vimAlias = true;

  #   withNodeJs = true;
  #   withPython3 = true;
  #   withRuby = true;
  # };
}
