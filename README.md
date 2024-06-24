## Setting up a new host

Create `hosts/$HOSTNAME/{configuration.nix,home.nix}` (if home-manager is to be
used).

### macOS

Install Homebrew if it's going to be used for the host (e.g. for apps that 
aren't available via nixpkgs).

```sh
# Will prompt installation of XCode CLI tools
git clone https://github.com/shimmerjs/.universe && cd .universe
hack/install-nix.nix
hack/init-darwin.sh
```

## Prior Art

- https://github.com/mitchellh/nixos-config for general layout and approach in a
  flakey world.
- [Activating `nix-darwin` settings on switch instead of next login](https://medium.com/@zmre/nix-darwin-quick-tip-activate-your-preferences-f69942a93236)