{ pkgs, ... }:
let
  # TODO: integrate this with flake.nix?
  sources = import ../../../nix/sources.nix;
in
{
  programs.zsh = with pkgs; {
    enable = true;
    autosuggestion.enable = true;
    enableCompletion = true;
    syntaxHighlighting.enable = true;
    initExtra = builtins.readFile ./zshrc;
    shellGlobalAliases = {
      # TODO: try to put these aliases in the relevant modules
      k = "kubectl";
      ksh = "kitty +kitten ssh";
      kcopy = "kitty +kitten clipboard";
      kpaste = "kitty +kitten clipboard --get-clipboard";
      j = "just";
      bzl = "bazel";
      # shortcut for showing images
      icat = "kitty +kitten icat --scale-up";
      # graphviz rendering with friendly settings for rendering in the terminal
      # can use alone to add more parameters for experimentation or one-off
      # changes
      tdot = "dot -Tsvg -Gfontname=courier -Gbgcolor=transparent -Grankdir=LR -Gratio=0.4 -Granksep=0.2 -Gnodesep=0.1 -Gconcentrate -Nfontsize=16 -Nshape=box -Nstyle=filled,rounded,bold -Ncolor=seagreen -Nfillcolor=palegreen3 -Nfontname=courier -Efontname=courier -Ecolor=peachpuff4";
      # shortcut for showing image rendered from default graphviz settings
      # for terminal friendly graphs
      idot = "tdot | icat";
      batdiff = "git diff --name-only --diff-filter=d | xargs bat --diff";
      # for pretty-fying streams of mixed garbage that contain JSON objects 
      jqmess = "jq -R 'fromjson? | .'";
    };
    shellAliases = {
      ls = "ls -A --color=auto";
    };
    history = {
      save = 1000000000;
      size = 1000000000;
      share = true;
      ignoreSpace = true;
      ignoreDups = true;
    };
    sessionVariables = {
      HYPHEN_INSENSITIVE = "true";
      DISABLE_AUTO_UPDATE = "true";
      COMPLETION_WAITING_DOTS = "false";
      # try to fix ansi colors
      # https://superuser.com/a/448892
      CLICOLOR = 1;
      GIT_SSL_CAINFO = "${cacert}/etc/ssl/certs/ca-bundle.crt";
      BAT_THEME = "OneHalfLight";
    };
    plugins = with sources; [
      {
        name = "powerlevel10k";
        file = "powerlevel10k.zsh-theme";
        src = fetchFromGitHub {
          inherit (sources.powerlevel10k) owner repo rev sha256;
        };
      }
    ];
  };

  # add generated p10k config file to correct location for zshrc to find it and
  # source it
  home.file.".config/p10k.zsh".source = ./p10k.zsh;
}
