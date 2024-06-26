## Setting up a new host

Create `hosts/$HOSTNAME/{configuration.nix,home.nix}` (if home-manager is to be
used).

### macOS

Initialize system without cloning the repo:

```sh
# Install XCode tools
xcode-select --install
# Install Homebrew
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
# Install Nix
sh <(curl -L https://releases.nixos.org/nix/nix-${NIX_VERSION:-'2.22.1'}/install)
# Initialize system
nix run nix-darwin \
  --extra-experimental-features nix-command \
  --extra-experimental-features flakes \
  -- switch --flake "github:shimmerjs/.universe#${HOSTNAME:-$(hostname)}"
```

To set up the `~/.universe` repo for pulling more updates and applying them by
hand, or tweaking that hosts config:

```sh
# Will prompt installation of XCode CLI tools
git clone https://github.com/shimmerjs/.universe $HOME/.universe && cd $HOME/.universe
hack/switch.sh
```

## Prior Art

- https://github.com/mitchellh/nixos-config for general layout and approach in a
  flakey world.
- [Activating `nix-darwin` settings on switch instead of next login](https://medium.com/@zmre/nix-darwin-quick-tip-activate-your-preferences-f69942a93236)
