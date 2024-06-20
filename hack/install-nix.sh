#!/usr/bin/env bash

# Installs Nix on non-NixOS hosts.

source $(dirname ${BASH_SOURCE[0]})/lib.sh

echo "installing nix@$NIX_VERSION"

if [[ "$OSTYPE" == "linux-gnu"* ]]; then
  FLAGS="--daemon"
elif [[ "$OSTYPE" == "darwin"* ]]; then
  FLAGS="--darwin-use-unencrypted-nix-store-volume"
else
  echo "man, idk"
  exit 1
fi

sh <(curl -L https://releases.nixos.org/nix/nix-$NIX_VERSION/install) "$FLAGS"
