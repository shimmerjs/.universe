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
    addOverlays = [
      (utils.standardPluginOverlay inputs)
    ];
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
        lspsAndRuntimeDeps = with pkgs; {
          general = [
            lazygit
            fd
            ripgrep
            fzf
            jq
            gh
          ];

          lua = [
            lua-language-server
            stylua
          ];

          nix = [
            nixd
            alejandra
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

          markdown = [
            marksman
          ];

          shell = [
            bash-language-server
            shellcheck
            shfmt
          ];

          yaml = [
            yaml-language-server
          ];

          json = [
            vscode-langservers-extracted
          ];

          cue = [
            cue
          ];
        };

        startupPlugins = {
          general = with pkgs.vimPlugins; [
            lze
            lzextras
            everforest
          ];
        };

        optionalPlugins = {
          general = with pkgs.vimPlugins; [
            vim-startuptime
            blink-cmp
            (nvim-treesitter.withPlugins (p: [
              p.go
              p.gomod
              p.gosum
              p.gowork
              p.nix
              p.lua
              p.luadoc
              p.bash
              p.hcl
              p.terraform
              p.yaml
              p.json
              p.markdown
              p.markdown_inline
              p.cue
              p.vim
              p.vimdoc
              p.regex
              p.query
              p.diff
              p.gitcommit
              p.toml
              p.dockerfile
              p.make
            ]))
            nvim-treesitter-textobjects
            nvim-treesitter-context
            mini-nvim
            nvim-lspconfig
            lualine-nvim
            lualine-lsp-progress
            gitsigns-nvim
            which-key-nvim

            # telescope + extensions
            plenary-nvim
            nvim-web-devicons
            telescope-nvim
            telescope-fzf-native-nvim
            telescope-ui-select-nvim
            pkgs.neovimPlugins.telescope-recent-files
            pkgs.neovimPlugins.telescope-switch
            telescope-github-nvim
            telescope-undo-nvim
            pkgs.neovimPlugins.adjacent-nvim
            octo-nvim
          ];
          lua = with pkgs.vimPlugins; [
            lazydev-nvim
          ];
        };
      };

    packageDefinitions.replace = {
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
            markdown = true;
            shell = true;
            yaml = true;
            json = true;
            cue = true;
          };
          extra = {
            nixdExtras.nixpkgs = "import ${pkgs.path}";
          };
        };
    };
  };
}
