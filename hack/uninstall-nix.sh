#!/usr/bin/env bash

# just putting the steps in a script
# https://nix.dev/manual/nix/2.18/installation/uninstall.html#macos

date=$(date '+%Y-%m-%d-%H:%M')
# back up rc files and create empty versions
sudo mv /etc/zshrc /etc/zshrc-bkp-"$date"
echo "backed up /etc/zshrc as /etc/zshrc-bkp-"$date""

sudo mv /etc/bashrc /etc/bashrc-bkp-"$date"
echo "backed up /etc/bashrc as /etc/bashrc-bkp-"$date""

sudo mv /etc/bash.bashrc /etc/bash.bashrc-bkp-"$date"
echo "backed up /etc/bash.bashrc  as /etc/bash.bashrc -bkp-"$date""

sudo touch /etc/zshrc /etc/bashrc /etc/bash.bashrc


sudo launchctl unload /Library/LaunchDaemons/org.nixos.nix-daemon.plist
sudo rm /Library/LaunchDaemons/org.nixos.nix-daemon.plist
sudo launchctl unload /Library/LaunchDaemons/org.nixos.darwin-store.plist
sudo rm /Library/LaunchDaemons/org.nixos.darwin-store.plist

sudo dscl . -delete /Groups/nixbld
for u in $(sudo dscl . -list /Users | grep _nixbld); do sudo dscl . -delete /Users/$u; done

# controversial but my fstab only has nix info
sudo mv /etc/fstab.bkp."$date"
sudo mv /etc/synthetic.conf.bkp."$date"

sudo rm -rf /etc/nix /var/root/.nix-profile /var/root/.nix-defexpr /var/root/.nix-channels ~/.nix-profile ~/.nix-defexpr ~/.nix-channels

sudo diskutil apfs deleteVolume /nix

echo "go ahead and restart your term just in case :)"