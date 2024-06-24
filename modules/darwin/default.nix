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

      # Don't show icons on the desktop
      CreateDesktop = false;
    };

    screencapture.location = "~/Pictures/screenshots";

    # Settings that aren't covered by nix-darwin APIs
    CustomUserPreferences = {
      "com.apple.desktopservices" = {
        # Limit DS_Store as much as possible
        DSDontWriteNetworkStores = true;
        DSDontWriteUSBStores = true;
      };
    };
  };

  # Automatically apply macOS preference changes without requiring login/logout
  system.activationScripts.postUserActivation.text = ''
    # Following line should allow us to avoid a logout/login cycle
    /System/Library/PrivateFrameworks/SystemAdministration.framework/Resources/activateSettings -u
  '';

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
    ];
  };
}
