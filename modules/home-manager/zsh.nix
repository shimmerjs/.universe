{ pkgs, ... }:
let
  # TODO: how to integrate this with flake.nix?
  sources = import ../../nix/sources.nix;
in
{
  programs.zsh = with pkgs; {
    enable = true;
    autosuggestion.enable = true;
    enableCompletion = true;
    syntaxHighlighting.enable = true;
    initExtra = ''
      # To customize prompt, run `p10k configure` or edit ~/.p10k.zsh.
      [[ ! -f $HOME/.config/p10k.zsh ]] || source $HOME/.config/p10k.zsh

      # only put cwd on tab/window title
      export DISABLE_AUTO_TITLE="true"
      precmd () {print -Pn "\e]0;%~\a"}

      # Add user controlled ad hoc bin directories to front of PATH, ensures that 
      # these binaries take precedence over nix-provided binaries when they conflict,
      # allowing quick and dirty testing of dev builds, etc.
      export PATH="$HOME/bin:$GOBIN:$PATH"
      
      # Configure keybindings to allow incremental history search
      # while using zsh-autosuggestions.
      bindkey "^[[A" history-beginning-search-backward
      bindkey "^[[B" history-beginning-search-forward
    '';
    shellGlobalAliases = {
      k = "kubectl";
      ksh = "kitty +kitten ssh";
      kcopy = "kitty +kitten clipboard";
      kpaste = "kitty +kitten clipboard --get-clipboard";
      bazel = "bazelisk";
      # Shortcut for showing images in the terminal
      icat = "kitty +kitten icat --scale-up";
      # Graphviz rendering with friendly settings for rendering in the terminal
      # can use alone to add more parameters for experimentation or one-off
      # changes
      tdot = "dot -Tsvg -Gfontname=courier -Gbgcolor=transparent -Grankdir=LR -Gratio=0.4 -Granksep=0.2 -Gnodesep=0.1 -Gconcentrate -Nfontsize=16 -Nshape=box -Nstyle=filled,rounded,bold -Ncolor=seagreen -Nfillcolor=palegreen3 -Nfontname=courier -Efontname=courier -Ecolor=peachpuff4";
      # Shortcut for showing image rendered from default graphviz settings
      # for terminal friendly graphs
      idot = "tdot | icat";
      batdiff = "git diff --name-only --diff-filter=d | xargs bat --diff";
      # For pretty-fying streams of mixed garbage that contain JSON objects 
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
      ignoreAllDups = true;
      expireDuplicatesFirst = true;
    };
    sessionVariables = {
      COMPLETION_WAITING_DOTS = "false";
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

  # Add generated p10k config file to correct location for zshrc to find it and
  # source it
  home.file.".config/p10k.zsh".source = ./p10k.zsh;

  programs.fzf = {
    enable = true;
    enableZshIntegration = true;
  };
}
