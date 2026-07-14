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
  #   bin/fancypants      PreToolUse(Write|Edit|MultiEdit|Bash) style guard --
  #     blocks decorative Unicode and banner/divider comments in file writes,
  #     and decorative Unicode in git-commit / gh pr/issue/release prose.
  #   bin/gocheck         Stop + SubagentStop gate -- `go build` + `go vet` the edited packages,
  #     using `go list -e -json` metadata to pick the buildable ones and
  #     `go vet -json` for structured findings; keeps the $GOCACHE cache.
  # doCheck runs the txtar suites at build time; gocheck integration tests that
  # spawn the toolchain are gated behind CLOD_GOCHECK_INTEGRATION and skip here.
  # gocheck shells out to the go toolchain (path pinned via ldflags), so the
  # output references ${pkgs.go} -- allowGoReference permits it. The -X symbol
  # is main.goBin: main packages link under package "main", not their import
  # path (an import-path pin is a silent no-op). Module-wide ldflags also hit
  # fancypants, which has no goBin and ignores it.
  glod = pkgs.buildGoModule {
    pname = "glod";
    version = "0";
    src = ./glod;
    vendorHash = null; # stdlib only
    doCheck = true;
    allowGoReference = true;
    ldflags = [ "-X main.goBin=${pkgs.go}/bin/go" ];
  };

  # PreToolUse(Workflow): deny name= invocations of deployed workflows -- the
  # name registry is frozen at session start, so name= can run a stale
  # pre-switch script; scriptPath reads the deployed symlink at invocation.
  awScriptpathGate = pkgs.writeShellApplication {
    name = "clod-aw-scriptpath-gate";
    runtimeInputs = with pkgs; [
      jq
      coreutils
    ];
    text = builtins.readFile ./aw-scriptpath-gate.sh;
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
