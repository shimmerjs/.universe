# OBS, khudson reno's capture engine. Darwin-scoped rather than part of
# module.nix because homebrew.casks is a system option and module.nix is
# home-manager -- but it belongs to khudson, so it lives here and the host
# imports it into systemConfig alongside the home-manager module.
#
# nix owns neither OBS nor its plugins and cannot: obs-studio.meta.platforms
# is linux-only, so a brew cask is the only route on darwin. That means the
# version floats -- the cask is unpinned and auto-updating -- and every
# plugin would be a hand-dropped bundle. reno is written against a stock OBS
# for exactly that reason.
{
  # /Applications, overriding the global caskArgs.appdir (~/Applications).
  # macOS refuses to activate a system extension whose host app lives
  # anywhere else -- OSSystemExtensionErrorUnsupportedParentBundleLocation,
  # "App containing System Extension to be activated must be in /Applications
  # folder" -- and ~/Applications has never counted, having never really been
  # part of the directory convention. It got stricter still in Sequoia.
  #
  # The consequence is not subtle and it is not loud: from ~/Applications
  # OBS's virtual camera extension can never install, so OBS's own UI happily
  # reports a virtual camera it has started while the extension behind it
  # does not exist. The tray shows it on, GetVirtualCamStatus honestly says
  # off, and reno's vcam tile reads off forever.
  #
  # Moving it costs a one-time TCC re-grant (screen recording, camera, mic)
  # because the bundle path changes, plus a manual approval in System
  # Settings > General > Login Items & Extensions > Camera Extensions.
  homebrew.casks = [
    {
      name = "obs";
      args = {
        appdir = "/Applications";
      };
    }
  ];
}
