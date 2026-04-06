#!/usr/bin/env bash

# attempt to run the install script from the repo
# if it fails run the local command 
sudo nix --extra-experimental-features "nix-command flakes" run nix-darwin#darwin-uninstaller
if [ $? -ne 0 ]; then
  sudo /run/current-system/sw/bin/darwin-uninstaller
