#! /usr/bin/env nix-shell
#! nix-shell -i bash -p nixpkgs-fmt

# Runs gen-vscode-extensions.sh and pipes it to VSCODE_EXTENSIONS_NIX or a 
# default path.

source $(dirname ${BASH_SOURCE[0]})/lib.sh

VSCODE_EXTENSIONS_NIX="${VSCODE_EXTENSIONS_NIX:-$UNIVERSE_PATH/modules/home-manager/vscode/extensions.nix}"

"$(dirname "${BASH_SOURCE[0]}")/gen-vscode-extensions.sh" | tee "$VSCODE_EXTENSIONS_NIX"
nixpkgs-fmt "$VSCODE_EXTENSIONS_NIX"