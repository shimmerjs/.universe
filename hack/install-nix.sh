#!/usr/bin/env bash

# Installs Nix on non-NixOS hosts.

source $(dirname ${BASH_SOURCE[0]})/lib.sh

echo "installing nix@$NIX_VERSION"

# NIX_FIRST_BUILD_UID needed for sequoia (https://github.com/NixOS/nix/issues/10892)
NIX_FIRST_BUILD_UID="351" sh <(curl -L https://releases.nixos.org/nix/nix-$NIX_VERSION/install) "$@"
