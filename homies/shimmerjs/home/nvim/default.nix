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
    luaPath = ./.;
    # nixCats-specific overlays (not applied to overall system)
    addOverlays = [
      # This will add any plugins in inputs named "plugins-pluginName" to
      # pkgs.neovimPlugins
      (utils.standardPluginOverlay inputs)
    ];
    # Which package definitions below to build
    packageNames = [ "nvim" ];

    categoryDefinitions.replace =
      {
        pkgs,
        settings,
        categories,
        extra,
        name,
        mkPlugin,
        ...
      }@packageDef:
      {
        # Available at runtime for plugins via PATH and
        # in nvim terminal.
        lspsAndRuntimeDeps = with pkgs; {
          general = [
            lazygit
            fd
            fzf
            jq
          ];

          lua = [
            lua-language-server
            stylua
          ];

          nix = [
            nixd
            # TODO: setup alejandra for fmting
          ];

          go = [
            gopls
            golint
            golangci-lint
            gotools
            go-tools
            go
          ];

          tf = [
            terraform-ls
          ];
        };

        # plugins unconditionally loaded at startup
        startupPlugins = {
          general = with pkgs.vimPlugins; [
            lze
            lzextras
          ];
        };

        # not loaded automatically at startup
        # used with packadd / autocommand in config for lazy loading
        optionalPlugins = {
          general = with pkgs.vimPlugins; [
            vim-startuptime
            blink-cmp
            nvim-treesitter.withAllGrammars # TODO: look into trimming to specific grammars
            mini-nvim
            nvim-lspconfig
            lualine-nvim
            lualine-lsp-progress
            gitsigns-nvim
            which-key-nvim
            # TODO: need these am i covered by lang specific lsps?
            # nvim-lint
            # conform-nvim
          ];
          lua = with pkgs.vimPlugins; [
            lazydev-nvim
          ];
        };
      };

    packageDefinitions.replace = {
      # named packages built from config above
      nvim =
        { pkgs, name, ... }:
        {
          settings = {
            wrapRc = true;
            aliases = [
              "vim"
              "vi"
              "neovim"
            ];
          };
          categories = {
            general = true;
            lua = true;
            nix = true;
            go = true;
            tf = true;
          };
          # anything else we want to pass to lua
          extra = {
            nixdExtras.nixpkgs = "import ${pkgs.path}";
          };
        };
    };
  };
}
