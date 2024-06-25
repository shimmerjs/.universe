{ ... }:
{
  imports = [
    ../nix
  ];

  # Used for backwards compatibility, please read the changelog before changing.
  # $ darwin-rebuild changelog
  system.stateVersion = 4;

  nix = {
    # We install Nix using a separate installer for macOS, this setting tells 
    # nix-darwin to just use whatever is running. It is also required for
    # multi-user builds, which is the default for all newer macOS nix 
    # installations.
    useDaemon = true;
    settings = {
      trusted-users = [ "root" "shimmerjs" ];
      allowed-users = [ "shimmerjs" ];
    };
  };

  programs.zsh.enable = true;

  users.users.shimmerjs = {
    name = "shimmerjs";
    # Explicitly set up user home directory to workaround nix-darwin issue:
    # https://github.com/LnL7/nix-darwin/issues/423
    home = "/Users/shimmerjs";
  };

  security = {
    pam = {
      enableSudoTouchIdAuth = true;
    };
  };

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

    screencapture.location = "/Users/shimmerjs/Pictures/screenshots";

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
        NewWindowTargetPath = "file:///Users/shimmerjs";
      };
    };

  };

  # Enable homebrew for all macOS hosts to install apps that aren't available
  # via nixpkgs
  homebrew = {
    enable = true;
    onActivation.cleanup = "uninstall"; # Clean up removed apps

    caskArgs = {
      appdir = "~/Applications"; # Use non-global directory
    };
    taps = [ ];
    brews = [ ];
    casks = [
      "flycut" # Clipboard manager
      "docker" # Tool for creation of human suffering
    ];
  };

  # Automatically apply macOS preference changes without requiring login/logout
  system.activationScripts.postUserActivation.text = ''
    # Following line should allow us to avoid a logout/login cycle
    /System/Library/PrivateFrameworks/SystemAdministration.framework/Resources/activateSettings -u
  '';
}
