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

  # One Go module (./glod) -> two hook binaries sharing an internal/txtar test
  # helper:
  #   bin/nofancyunicode  PreToolUse(Write|Edit|MultiEdit|Bash) ASCII guard --
  #     blocks decorative Unicode in file writes and in git-commit / gh
  #     pr/issue/release authored prose.
  #   bin/gocheck         Stop gate -- `go build` + `go vet` the edited packages,
  #     using `go list -e -json` metadata to pick the buildable ones and
  #     `go vet -json` for structured findings; keeps the $GOCACHE cache.
  # doCheck runs the txtar suites at build time; gocheck integration tests that
  # spawn the toolchain are gated behind CLOD_GOCHECK_INTEGRATION and skip here.
  # gocheck shells out to the go toolchain (path pinned via ldflags), so the
  # output references ${pkgs.go} -- allowGoReference permits it.
  glod = pkgs.buildGoModule {
    pname = "glod";
    version = "0";
    src = ./glod;
    vendorHash = null; # stdlib only
    doCheck = true;
    allowGoReference = true;
    ldflags = [ "-X glod/cmd/gocheck.goBin=${pkgs.go}/bin/go" ];
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
}
