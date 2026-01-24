{
  pkgs,
  inputs,
  lib,
  ...
}:
{
  programs.kitty = {
    enable = true;
    themeFile = "everforest_dark_soft";

    settings.kitty_mod = "ctrl+opt+shift";
    keybindings = {
      # TODO: figure out how to use action_alias to DRY out `launch --cwd=current` etc

      # Docs
      "f1" = "show_kitty_doc conf";

      # OS windows
      "cmd+n" = "new_os_window";

      # Splits / "kitty windows"
      "cmd+enter" = "new_window_with_cwd";
      "cmd+]" = "next_window";
      "cmd+[" = "previous_window";
      "cmd+w" = "close_window";
      "cmd+1" = "first_window";
      "cmd+2" = "second_window";
      "cmd+3" = "third_window";
      "cmd+4" = "fourth_window";
      "cmd+5" = "fifth_window";
      "cmd+6" = "sixth_window";
      "cmd+r" = "start_resizing_window";
      "ctrl+shift+f" = "move_window_forward";
      "ctrl+shift+b" = "move_window_backward";
      "ctrl+shift+t" = "move_window_to_top";

      # Split management specific to the 'splits' kitty layout
      #
      # Additional splits layout mappable actions not currently used:
      #
      # move_window {up,left,right,down}
      # layout_action move_to_screen_edge {top,left,right,bottom}
      # neighboring_window {up,left,right,down} (switch focus to the neighboring window in the indicated direction)
      "kitty_mod+enter" = "launch --location=hsplit --cwd=current"; # One above the other
      "kitty_mod+cmd+enter" = "launch --location=vsplit --cwd=current"; # One beside the other
      "ctrl+shift+enter" = "launch --location=split --cwd=current"; # Dynamic based on the current split
      "ctrl+shift+r" = "layout_action rotate"; # Rotate orientation of active split

      # Tab management
      "cmd+shift+]" = "next_tab";
      "cmd+shift+[" = "previous_tab";
      "cmd+shift+w" = "close_tab";
      "cmd+shift+e" = ''
        launch --type=overlay --allow-remote-control ${
          lib.getExe inputs.kitty-tab-switcher.packages.${pkgs.stdenv.hostPlatform.system}.default
        }
      '';

      # Scrollback and terminal content management / helpers
      # https://sw.kovidgoyal.net/kitty/#the-scrollback-buffer
      # TODO: use modals/overlay windows for some scrollback funs
      "cmd+shift+h" = "show_scrollback";
      "ktty_mod+c" = "copy_last_command_output";

      # Reset terminal
      # TODO: leverage ability to clear into the scrollback?
      "cmd+k" = "clear_terminal clear active";
      "cmd+option+k" = "clear + terminal scroll active";

      # Layouts
      "cmd+shift+l" = "next_layout";

      # Font sizes
      "cmd+shift+equal" = "change_font_size current + 2.0";
      "cmd+minus" = "change_font_size current - 2.0";
      "cmd+opt+shift+equal" = "change_font_size all + 2.0";
      "cmd+opt+minus" = "change_font_size all - 2.0";
    };

    settings = {
      # Dont update unless its via Nix
      update_check_interval = 0;
      enable_audio_bell = "no";
      # Enable reading and writing from clipboard
      clipboard_control = "write-clipboard read-clipboard write-primary read-primary";

      confirm_os_window_close = 0;

      allow_hyperlinks = "yes";
      open_url_modifiers = "cmd";

      # Session bits
      startup_session = "sessions/default.conf";

      # macOS specific bits
      macos_quit_when_last_window_closed = "yes";
      macos_thicken_font = "0.10";
      macos_show_window_title_in = "menubar";
      macos_option_as_alt = "yes"; # Make ALT-_ keybindings work

      # Appearance
      tab_bar_margin_width = "5.0";
      tab_bar_style = "powerline";

      window_border_width = "0.60pt";

      font_size = "16.0";
      font_family = "FiraCode Nerd Font Mono";

      enabled_layouts = "splits:split_axis=vertical,fat:bias=50;full_size=2,vertical";

      # Scrollback
      scrollback_lines = 5000;
      # TODO: use normal pager like less here and use fzf for the scrollback search
      # pager specifically?
      scrollback_pager = "fzf --ansi --no-bold";
      scrollbar_gap = "0.2";
    };

    quickAccessTerminalConfig = {
      edge = "left";
      width = "180";
      hide_on_focus_loss = "yes";
      background_opacity = "0.9";
    };
  };

  # Wire up static kitty assets not managed by home-manager/nix
  home.file = {
    ".config/kitty/sessions" = {
      recursive = true;
      source = ./sessions;
    };
    ".config/kitty/choose-files.conf".text = ''
      show_hidden = "true";
      sort_by_last_modified = "true";
      respect_ignores = "true";
      show_preview = "true";
    '';
  };
}
