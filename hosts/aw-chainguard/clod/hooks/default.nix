# writeShellApplication-wrapped hook programs for clod, with nix-pinned binaries
# (runtimeInputs) so they never depend on PATH. Imported by ../default.nix; each
# wraps the like-named script in this directory.
{ pkgs }:
{
  # PostToolUse: gofmt syntax gate + goimports per .go edit, queue for the Stop pass.
  goFmtHook = pkgs.writeShellApplication {
    name = "clod-go-fmt-hook";
    runtimeInputs = with pkgs; [
      go
      gotools
      jq
      coreutils
    ];
    text = builtins.readFile ./go-fmt-hook.sh;
  };

  # Stop: batched `go build` then `go vet` on the edited packages (compiler is truth).
  goCheckHook = pkgs.writeShellApplication {
    name = "clod-go-check-hook";
    runtimeInputs = with pkgs; [
      go
      jq
      coreutils
    ];
    text = builtins.readFile ./go-check-hook.sh;
  };

  # SessionEnd: deterministic git breadcrumb to the OS temp dir, so a session that
  # ended without /handoff still leaves a recoverable pointer.
  handoffSnapshotHook = pkgs.writeShellApplication {
    name = "clod-handoff-snapshot-hook";
    runtimeInputs = with pkgs; [
      git
      jq
      coreutils
    ];
    # SC2016 fires on the literal markdown code-fence backticks in the printf
    # format strings -- intentional: the data is passed as properly-quoted printf
    # args, never expanded in the format. False positive, so exclude just this check.
    excludeShellChecks = [ "SC2016" ];
    text = builtins.readFile ./handoff-snapshot-hook.sh;
  };

  # PostToolUse(Bash): sweep stray `go build` binaries out of the tree into a
  # gitignored .claude/bin/ so they can't be committed.
  goBuildSweep = pkgs.writeShellApplication {
    name = "clod-go-build-sweep";
    runtimeInputs = with pkgs; [
      git
      file
      jq
      coreutils
    ];
    text = builtins.readFile ./go-build-sweep.sh;
  };

  # PreToolUse(Write|Edit|MultiEdit): block decorative Unicode in new content.
  # ASCII only, in every project.
  noFancyUnicodeHook = pkgs.writeShellApplication {
    name = "clod-no-fancy-unicode";
    runtimeInputs = [ ];
    text = "exec ${pkgs.python3}/bin/python3 ${./no-fancy-unicode.py}";
  };
}
