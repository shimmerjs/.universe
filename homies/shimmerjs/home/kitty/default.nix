{
  pkgs,
  inputs,
  lib,
  config,
  ...
}:
let
  # Build the krib cheatsheet/palette engine (single source of truth:
  # pkgs/krib, binary `krib`).
  krib = (
    pkgs.buildGoModule {
      pname = "krib";
      src = ../../../../pkgs/krib;
      version = "0.1.0";
      vendorHash = "sha256-Gc1vL8/qJczgi8TUAhLwLOxvvGsMAaqvq7/OjPEIvj0=";
      # guard the classify/chord/parity tests at build (they ran under no
      # check before, which let the forked category tables drift).
      doCheck = true;
    }
  );

  # nix-set default for the palette's show-all view (the per-session toggle
  # lives on ctrl-a inside fzf).
  paletteShowAll = false;

  # ONE launcher path for every palette summon trigger (f3, the right-press
  # mouse_map, and remote control): scrape the live bindings once into a
  # session cache, then hand off to `krib palette` (fzf frontend; accept
  # executes through the sheet's exec descriptor, targeting the parent
  # window passed as $1).
  #
  # Remote-control summon, from any process with kitty rc access (the seam a
  # future khudson dock act will call):
  #   kitten @ launch --type=overlay --allow-remote-control -- \
  #     krib-palette @active-kitty-window-id
  # (@active-kitty-window-id expands to the summoning window's id -- the
  # overlay's parent -- exactly like the f3/mouse bindings below.)
  krib-palette = pkgs.writeShellApplication {
    name = "krib-palette";
    runtimeInputs = [
      krib
      pkgs.fzf
    ];
    text = ''
      cache=$(mktemp)
      trap 'rm -f "$cache"' EXIT
      kitten @ kitten kits/keybindings.py > "$cache"
      args=(palette --data "$cache")
      ${lib.optionalString paletteShowAll ''args+=(--all)''}
      if [ $# -ge 1 ] && [ -n "$1" ]; then
        args+=(--window "$1")
      fi
      krib "''${args[@]}"
    '';
  };
in
{
  programs.kitty = {
    enable = true;
    themeFile = "everforest_dark_soft";

    settings.kitty_mod = "ctrl+opt+shift";
    keybindings = {
      # Reload configuration
      "cmd+ctrl+," = "load_config_file";

      # Docs
      "f1" = "show_kitty_doc conf";
      # Show current keybindings
      "f2" =
        "launch --type=overlay --hold --allow-remote-control sh -c \"kitten @ kitten kits/keybindings.py | ${krib}/bin/krib print\"";
      # krib palette: actionable fzf palette over the live bindings.
      # @active-kitty-window-id is the summoning (parent) window, so accepted
      # window-class actions target it, not the overlay.
      "f3" = "launch --type=overlay --allow-remote-control ${lib.getExe krib-palette} @active-kitty-window-id";
      "kitty_mod+f3" = "command_palette";
      # clod workflow cheatsheet, rendered from each aw-*.js meta.flags
      "f4" = ''launch --type=overlay sh -c "clod-cheat | less -R"'';
      # kuiboard: standalone Moonlander keyboard TUI (couch-mode keyboard
      # panel); dials the HID daemon keys.sock, no Edge/dock needed.
      "f5" = "launch --type=os-window --title kuiboard kuiboard";

      # Clipboard
      "cmd+c" = "copy_or_noop";
      "cmd+v" = "paste_from_clipboard";

      # OS windows
      "cmd+n" = "new_os_window";
      "cmd+q" = "quit";
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
      # Layouts
      "cmd+shift+l" = "next_layout";

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
      "cmd+t" = "new_tab_with_cwd";
      "cmd+shift+]" = "next_tab";
      "cmd+shift+[" = "previous_tab";
      "cmd+shift+w" = "close_tab";
      "kitty_mod+." = "move_tab_forward";
      "kitty_mod+," = "move_tab_backward";
      "cmd+shift+e" = ''
        launch --type=overlay --allow-remote-control ${
          lib.getExe inputs.kitty-tab-switcher.packages.${pkgs.stdenv.hostPlatform.system}.default
        }
      '';

      # Scrollback and terminal content management / helpers
      # https://sw.kovidgoyal.net/kitty/#the-scrollback-buffer
      # TODO: use modals/overlay windows/slits for some scrollback funs
      # TODO: action for clearing last command
      # TODO: search_scrollback
      "cmd+shift+h" = "show_scrollback";
      "cmd+shift+g" = "show_last_non_empty_command_output";
      "kitty_mod+c" = "copy_last_command_output";

      # Reset terminal
      # TODO: leverage ability to clear into the scrollback?
      "cmd+k" = "clear_terminal clear active";
      "cmd+option+k" = "clear_terminal scroll active";
      "cmd+l" = "clear_terminal last_command active";

      # Scrolling
      # TODO: use vim arrows?
      # TODO: configure shortcuts for
      #   scroll_home, scroll_end
      #   scroll_to_prompt -1 (previous command)
      #   scroll_to_prompt 1 (next command)
      "cmd+up" = "scroll_line_up";
      "cmd+down" = "scroll_line_down";
      "cmd+page_up" = "scroll_page_up";
      "cmd+page_down" = "scroll_page_down";

      # Font sizes
      # allow '+' or '=' for increasing font size
      "cmd+plus" = "change_font_size all + 2.0";
      "cmd+equal" = "change_font_size all + 2.0";
      "cmd+minus" = "change_font_size all - 2.0";
      # Reset font size
      "cmd+0" = "change_font_size all 0";
    };

    # Mouse summon for the krib palette: right-press while the program has
    # not grabbed the mouse.
    mouseBindings = {
      "right press" = "ungrabbed launch --type=overlay --allow-remote-control ${lib.getExe krib-palette} @active-kitty-window-id";
    };

    settings = {
      # Ensure that Nix-managed binaries are available to kitty actions
      env = "PATH=${config.home.profileDirectory}/bin:/run/current-system/sw/bin:$PATH";
      # Required to automate kitty
      allow_remote_control = "yes";
      # Dont update unless its via Nix
      update_check_interval = 0;
      # Take full control of keybindings
      clear_all_shortcuts = "yes";
      enable_audio_bell = "no";

      # Enable reading and writing from clipboard
      clipboard_control = "write-clipboard read-clipboard write-primary read-primary";

      confirm_os_window_close = 0;
      remember_window_position = "yes";

      allow_hyperlinks = "yes";

      # Session bits
      startup_session = "sessions/default.conf";

      # macOS specific bits
      macos_show_window_title_in = "menubar";
      macos_option_as_alt = "yes"; # Make ALT-_ keybindings work
      macos_titlebar_color = "background";

      # Appearance
      tab_bar_margin_width = "5.0";
      tab_bar_style = "powerline";

      window_border_width = "0.60pt";

      font_size = "16.0";
      font_family = "FiraCode Nerd Font Mono";
      disable_ligatures = "always"; # render ->, =>, !=, etc. as plain glyphs

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
      columns = "180";
      hide_on_focus_loss = "yes";
      background_opacity = "0.9";
    };
  };

  # Nix's makeCWrapper replaces kitty's main executable with a wrapper binary
  # that exec's .kitty-wrapped. macOS AMFI kills this on Launch Services launch
  # unless the bundle is signed with hardened runtime.
  home.activation.signKitty = lib.mkIf pkgs.stdenv.hostPlatform.isDarwin (
    lib.hm.dag.entryAfter [ "linkGeneration" ] ''
      run /usr/bin/codesign --force --deep --sign - --options runtime \
        "$HOME/Applications/Home Manager Apps/kitty.app"
    ''
  );

  # Wire up static kitty assets not generated by home-manager/nix
  home.file = {
    ".config/kitty/sessions" = {
      recursive = true;
      source = ./sessions;
    };
    ".config/kitty/kits" = {
      recursive = true;
      source = ./kits;
    };
    ".config/kitty/choose-files.conf".text = ''
      show_hidden true
      sort_by_last_modified true
      respect_ignores true
      show_preview true
    '';
  };
}
