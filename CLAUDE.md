# Working in this repo

`.universe` is a Nix flake -- nix-darwin + home-manager host and userland definitions.

- `hosts/` -- per-machine configs (`hosts/<host>/default.nix` or `hosts/<host>.nix`).
- `homies/` -- per-user home-manager configs.
- `modules/` -- shared darwin / home-manager modules.
- `lib/` -- flake helpers (`mksystem.nix`, `mkchecks.nix`, check builders).
- Flake outputs: `darwinConfigurations`, `nixosConfigurations`, `checks`.

## VALIDATION GOES THROUGH NIX -- NEVER ARBITRARY EXTERNAL COMMANDS

Validate a change exactly one of two ways:

1. **Author a check in the nix config and run it.** Add or extend a flake check (see `lib/mkchecks.nix` -- checks are auto-discovered per host), then `nix flake check` or `nix build .#checks.<system>.<name>`.
2. **Build the config in question.** `nix build .#darwinConfigurations.<host>.system` (or the relevant attr) to prove it evaluates and builds.

Do **not** validate by running ad-hoc external commands (`node x.js`, `python lint.py`, a bare `go test`, ...) against the working tree. If something is worth checking, it is worth encoding as a nix check so it is reproducible and runs under `nix flake check`.

## TOOLS: NIX SHELL, NOT GLOBAL INSTALLS

- **One-off exploration** needing a tool that isn't in the config: `nix shell nixpkgs#<tool> --command <cmd>` (or `nix run nixpkgs#<tool>`). Don't assume a tool is on `PATH`; don't install globally.
- **Recurring task:** don't keep re-running an ad-hoc command -- create a nix shell / devShell (or a flake check/app) and run the task there, so it's captured in the config and reproducible.

## FLAKE GOTCHAS

- Nix only sees **git-tracked** files. `git add` new files before `nix eval` / `nix build` / `nix flake check` will see them -- untracked files are invisible to the flake even on a dirty tree.
- Edit the nix sources, not deployed files under `~`. Changes land on `darwin-rebuild switch` / home-manager activation.
