# Go editor/LSP toolchain, shared single source of truth: go.nix installs it
# on PATH via home.packages; vscode-go.nix exposes it to the vscode Go
# extension via go.toolsGopath. Tools are resolved by binary name, so changes
# here are the only maintenance point -- no per-tool path map.
pkgs: with pkgs; [
  gopls
  delve
  gotools # goimports -- used by the clod go hooks, and generally useful
  go-tools # staticcheck
  golangci-lint
  gotests
  gomodifytags
  impl
]
