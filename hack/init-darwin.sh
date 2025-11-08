#!/usr/bin/env bash

# Initial system build and switch for macOS hosts. After this is ran, `switch.sh`
# can be used.

source $(dirname ${BASH_SOURCE[0]})/lib.sh

nix run nix-darwin \
  --extra-experimental-features nix-command \
  --extra-experimental-features flakes \
  -- switch --flake ".#$(HOSTNAME)"
  # -- switch --flake "github:shimmerjs/.universe#${HOSTNAME:-$(hostname)}"
