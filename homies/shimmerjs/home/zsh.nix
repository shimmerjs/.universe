# TODO: move more stuff out into dev.nix
{
  inputs,
  lib,
  ...
}:
{
  programs.zsh = {
    enable = true;
    autosuggestion.enable = true;
    enableCompletion = true;
    # Full compinit (fpath scan + compaudit) at most daily; -C trusts the
    # existing dump otherwise. The system-level compinit is disabled in
    # modules/darwin so this is the only one and the dump stays stable.
    completionInit = ''
      autoload -U compinit
      if [[ -n $(find "''${ZDOTDIR:-$HOME}/.zcompdump" -mtime -1 2>/dev/null) ]]; then
        compinit -C
      else
        compinit
      fi
    '';
    syntaxHighlighting.enable = true;
    # zsh keys off $EDITOR containing "vi" otherwise; pin against an editor
    # change silently flipping the shell to vi bindings.
    defaultKeymap = "emacs";
    initContent = lib.mkMerge [
      # p10k instant prompt paints the last-known prompt immediately while
      # the rest of init runs behind it; it must precede anything that can
      # print, so it rides order 500 (before compinit and plugins).
      (lib.mkOrder 500 ''
        if [[ -r "''${XDG_CACHE_HOME:-$HOME/.cache}/p10k-instant-prompt-''${(%):-%n}.zsh" ]]; then
          source "''${XDG_CACHE_HOME:-$HOME/.cache}/p10k-instant-prompt-''${(%):-%n}.zsh"
        fi
      '')
      ''
        # To customize prompt, run `p10k configure` or edit ~/.p10k.zsh.
        [[ ! -f $HOME/.config/p10k.zsh ]] || source $HOME/.config/p10k.zsh

        # smart-case tab completion: exact matches first, then typed lowercase
        # matches uppercase candidates (doc<TAB> -> Documents); typed uppercase
        # stays exact. Explicit because bare compinit is case-sensitive.
        zstyle ':completion:*' matcher-list "" 'm:{a-z}={A-Za-z}'

        # only put cwd on tab/window title
        precmd () {print -Pn "\e]0;%~\a"}

        # Configure keybindings to allow incremental history search
        # while using zsh-autosuggestions.
        bindkey "^[[A" history-beginning-search-backward
        bindkey "^[[B" history-beginning-search-forward
      ''
    ];
    shellAliases = {
      ls = "ls -A --color=auto";
      clod = "claude";
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
      # For pretty-fying streams of mixed garbage that contain JSON objects
      jqmess = "jq -R 'fromjson? | .'";
      # User-specific universe aliases
      uedit = "code $UNIVERSE_PATH";
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
      BAT_THEME = "OneHalfLight";
    };
    plugins = [
      {
        name = "powerlevel10k";
        file = "powerlevel10k.zsh-theme";
        src = inputs.powerlevel10k;
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
