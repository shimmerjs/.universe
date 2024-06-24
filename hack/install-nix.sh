#!/usr/bin/env bash

# Installs Nix on non-NixOS hosts.

source $(dirname ${BASH_SOURCE[0]})/lib.sh

echo "installing nix@$NIX_VERSION"

sh <(curl -L https://releases.nixos.org/nix/nix-$NIX_VERSION/install) "$@"
