{
  programs.kitty = {
    enable = true;
    themeFile = "everforest_dark_soft";
    quickAccessTerminalConfig = {
      edge = "left";
      width = "180";
      hide_on_focus_loss = "yes";
      background_opacity = "0.9";
    };
    settings = {
      # Dont update unless its via Nix
      update_check_interval = 0;

      startup_session = "sessions/default.conf";
      macos_quit_when_last_window_closed = "yes";
      macos_thicken_font = "0.10";
      macos_show_window_title_in = "menubar";
      macos_option_as_alt = "yes"; # Make ALT-_ keybindings work

      tab_bar_margin_width = "5.0";
      tab_bar_style = "powerline";

      window_border_width = "0.60pt";

      font_size = "16.0";
      font_family = "FiraCode Nerd Font Mono";

      enable_audio_bell = "no";

      allow_hyperlinks = "yes";
      open_url_modifiers = "cmd";

      scrollback_lines = 50000;
      scrollback_pager = "fzf --ansi --no-bold";

      enabled_layouts = "fat:bias=50;full_size=2,vertical";

      # Enable reading and writing from clipboard
      clipboard_control = "write-clipboard read-clipboard write-primary read-primary";
    };
    keybindings = {
      # Tabs
      "cmd+shift+right" = "next_tab";
      "cmd+shift+left" = "previous_tab";
      "cmd+shift+w" = "close_tab";
      "cmd+shift+1" = "goto_tab 1";
      "cmd+shift+2" = "goto_tab 2";
      "cmd+shift+3" = "goto_tab 3";
      "cmd+shift+4" = "goto_tab 4";
      "cmd+shift+5" = "goto_tab 5";
      "cmd+shift+6" = "goto_tab 6";

      # Window management
      "cmd+s" = "new_window_with_cwd";
      "cmd+right" = "next_window";
      "cmd+left" = "previous_window";
      "cmd+w" = "close_window";
      "cmd+1" = "first_window";
      "cmd+2" = "second_window";
      "cmd+3" = "third_window";
      "cmd+4" = "fourth_window";
      "cmd+5" = "fifth_window";
      "cmd+6" = "sixth_window";

      # Layouts
      "cmd+shift+l" = "next_layout";

      # Font sizes
      "cmd+equal" = "change_font_size current + 2.0";
      "cmd+shift+equal" = "change_font_size all + 2.0";
      "cmd+minus" = "change_font_size current - 2.0";
      "cmd+shift+minus" = "change_font_size all - 2.0";

      # https://sw.kovidgoyal.net/kitty/#the-scrollback-buffer
      "cmd+shift+h" = "show_scrollback";

      # Reset terminal
      "cmd+k" = "clear_terminal clear active";
      "ctrl+k" = "clear + terminal scroll active";
    };
  };
  # Wire up static kitty assets not managed by home-manager/nix
  home.file.".config/kitty/sessions" = {
    recursive = true;
    source = ./sessions;
  };
}
