{
  # nix-darwin configuration
  systemConfig =
    { pkgs
    , lib
    , user
    , ...
    }:
    {
      imports = [
        ../../modules/darwin
        ../../modules/darwin/homebrew.nix
      ];

      # Add fonts for development
      # TODO: move into dev-specific profile, some macOS hosts arent used for dev
      fonts.packages = builtins.filter lib.attrsets.isDerivation (builtins.attrValues pkgs.nerd-fonts);

      system.primaryUser = user;

      system.defaults = {
        NSGlobalDomain = {
          # Global finder config, probably redundant with finder-specific
          # config below
          AppleShowAllExtensions = true;
          AppleShowAllFiles = true;

          # Don't correct me
          NSAutomaticCapitalizationEnabled = false;
          NSAutomaticDashSubstitutionEnabled = false;
          NSAutomaticPeriodSubstitutionEnabled = false;
          NSAutomaticQuoteSubstitutionEnabled = false;
          NSAutomaticSpellingCorrectionEnabled = false;
        };

        dock = {
          appswitcher-all-displays = true;
          autohide = true;
          autohide-delay = 0.15;
          launchanim = false;
          show-recents = false;
          # Only show active apps
          static-only = true;
        };

        spaces = {
          spans-displays = false;
        };

        finder = {
          AppleShowAllExtensions = true;
          AppleShowAllFiles = true;
          ShowPathbar = true;
          ShowStatusBar = true;
          FXPreferredViewStyle = "Nlsv"; # Default to list view
          FXDefaultSearchScope = "SCcf"; # Search current folder
          # Don't show icons on the desktop
          CreateDesktop = false;
        };

        screencapture.location = "/Users/scott/Pictures/screenshots";

        # Settings that aren't covered by nix-darwin APIs
        CustomUserPreferences = {
          "com.apple.desktopservices" = {
            # Limit DS_Store as much as possible
            DSDontWriteNetworkStores = true;
            DSDontWriteUSBStores = true;
          };

          "NSGlobalDomains" = {
            # Make key-repeat more responsive + faster by default, defined here because
            # we can define the fractional values as strings.
            #
            # system.defaults.NSGlobalDomain counterpart is typed as int.
            InitialKeyRepeat = 10; # How soon key repeating starts, default is 15 or 225ms
            KeyRepeat = 0.5; # How quickly key repeats, default is 2 or 30ms
          };

          # Finder settings not covered by nix-darwin module...
          # TODO: move all to this section so I can just deal with preferences
          # structure instead of whatever nix-darwin exposes _and_ preferences
          "com.apple.finder" = {
            # Always keep folders at top, even when sorting files by name
            _FXSortFoldersFirst = true;
            # Make new finder windows open up in ~/
            NewWindowTarget = "PfLo";
            NewWindowTargetPath = "file:///Users/scott";
          };
        };
      };

      homebrew = {
        casks = [
          "flycut" # Clipboard manager
          "spotify"
          "docker"
          "bitwarden" # Bitwarden GUI
          "keymapp" # Manage ergodox-ez keyboard graphically
          # "logi-options+" # Manage my mouse, ideally
          "signal" # Holla at ya boy
        ];
      };
    };

  # home-manager config for this homie that is only applied to darwin hosts.
  home =
    { pkgs
    , config
    , lib
    , ...
    }:
    {
      imports = [
        ./home/kitty
        ./home/vscode
        ../../modules/home-manager/rectangle.nix
      ];

      home.packages = with pkgs; [
        # telegram-desktop
        wally-cli # Manage ergodox-ez keyboard
      ];

      universe.home.rectangle = {
        enable = true;
        configFile.source = ./home/prefs/com.knollsoft.Rectangle.plist;
      };
    };
}
