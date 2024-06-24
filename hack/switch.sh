#!/usr/bin/env bash

source $(dirname ${BASH_SOURCE[0]})/lib.sh

cmd="$(os_switch 'nixos-rebuild' 'darwin-rebuild')"

"$cmd" switch --flake ".#$(HOSTNAME)" "$@"
