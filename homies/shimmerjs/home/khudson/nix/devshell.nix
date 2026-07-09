# khudson dev-lifecycle devShell, entered via the project-local ../shell.nix
# (nix-shell from the khudson dir). Deliberately NOT wired into the root
# flake.
#
# WHY THIS EXISTS: iteration convenience with the full toolset. Validation
# itself is build-local -- module.nix builds khudson/touchd with doCheck = true
# and host-tool tests skip-on-missing -- but a shell that HAS the tools on
# PATH (nix-provided plus the macOS /usr/bin builtins: top/vm_stat/sysctl
# for cpumem+procs, m1ddc for brightness, gh for githubprs, kitten for
# kittysessions, btop/kitty/sqlite3 for the live/db paths) executes the
# skipped tests too, and carries the khudson-* task scripts.
#
# top/vm_stat/ps/osascript/lsappinfo/sysctl are macOS /usr/bin builtins -- NOT
# nix-packaged; the shell just keeps /usr/bin on PATH (the default nix develop
# shell does).
{ pkgs }:
let
  # Module roots, resolved at RUN time relative to the git repo, so the scripts
  # edit/build the live working tree rather than a frozen store copy and work
  # from any cwd inside the shell. KHUDSON_MODULE_ROOT (set by the shellHook) wins;
  # otherwise fall back to <repo>/homies/shimmerjs/home/khudson/<mod>.
  khudsonRootExpr = ''"''${KHUDSON_MODULE_ROOT:-$(git rev-parse --show-toplevel)/homies/shimmerjs/home/khudson/khudson}"'';

  # go test ./... from the khudson module root; host tools present -> green.
  khudsonTest = pkgs.writeShellApplication {
    name = "khudson-test";
    runtimeInputs = [ pkgs.go ];
    text = ''
      cd ${khudsonRootExpr}
      exec go test "$@" ./...
    '';
  };

  # Race detector on the two concurrency-heavy packages (bus + dock).
  khudsonRace = pkgs.writeShellApplication {
    name = "khudson-race";
    runtimeInputs = [ pkgs.go ];
    text = ''
      cd ${khudsonRootExpr}
      exec go test -race "$@" ./internal/bus/ ./internal/dock/
    '';
  };

  # The deployable binary.
  khudsonBuild = pkgs.writeShellApplication {
    name = "khudson-build";
    runtimeInputs = [ pkgs.go pkgs.coreutils ];
    text = ''
      cd ${khudsonRootExpr}
      mkdir -p /tmp/khudson
      exec go build -o /tmp/khudson/khudson .
    '';
  };

  # Build + vet.
  khudsonVet = pkgs.writeShellApplication {
    name = "khudson-vet";
    runtimeInputs = [ pkgs.go pkgs.coreutils ];
    text = ''
      cd ${khudsonRootExpr}
      mkdir -p /tmp/khudson
      go build -o /tmp/khudson/khudson .
      exec go vet ./...
    '';
  };

  # Env-gated live tests. Each gate + its packages:
  #   KHUDSON_KEYMAPP_DB=1  -> ./internal/dock (real Keymapp layout render) and
  #                         ./internal/keyboard/keymappdb (reads the sqlite
  #                         Keymapp store; needs sqlite3).
  #   KHUDSON_CLAUDE_LIVE=1 -> ./internal/module/claudesessions (live host poll).
  #   KHUDSON_SPIKE1=1      -> ./internal/bus (spawns a GUI kitty + btop; the
  #                         busdock/spike1 live e2e).
  #   KHUDSON_AX=1       -> ./internal/ax (live Dock AX walk/press; needs the
  #                         Accessibility grant, and the press leg really
  #                         restores the first minimized window).
  # KHUDSON_SPIKE1_BTOP can override the btop binary; we default it to the
  # nix-provided one so the spike finds btop without PATH surgery.
  khudsonLive = pkgs.writeShellApplication {
    name = "khudson-live";
    runtimeInputs = [ pkgs.go ];
    text = ''
      cd ${khudsonRootExpr}
      export KHUDSON_KEYMAPP_DB=1
      export KHUDSON_CLAUDE_LIVE=1
      export KHUDSON_SPIKE1=1
      export KHUDSON_AX=1
      export KHUDSON_SPIKE1_BTOP="''${KHUDSON_SPIKE1_BTOP:-${pkgs.btop}/bin/btop}"
      exec go test -v "$@" \
        ./internal/ax/ \
        ./internal/dock/ \
        ./internal/keyboard/keymappdb/ \
        ./internal/module/claudesessions/ \
        ./internal/bus/
    '';
  };

  # Recompute the go-modules vendorHash for khudson AND touchd after a dep bump.
  # Build (buildGoModule {... vendorHash = fakeHash;}).goModules for each; the
  # build fails with the got hash, which we grep out and print. The tree fetches
  # deps via vendorHash (no committed vendor/), so this is the recompute helper.
  khudsonVendorhash = pkgs.writeShellApplication {
    name = "khudson-vendorhash";
    runtimeInputs = [ pkgs.nix pkgs.gnugrep pkgs.gnused pkgs.git ];
    text = ''
      root="''${KHUDSON_REPO_ROOT:-$(git rev-parse --show-toplevel)}"

      recompute() {
        mod="$1"
        # Untracked files are invisible to the flake; the FOD build reads the
        # committed src, so warn if the module tree has uncommitted go.mod/go.sum.
        expr="(builtins.getFlake (toString $root)).inputs.nixpkgs.legacyPackages.${pkgs.stdenv.hostPlatform.system}.buildGoModule {
          pname = \"$mod-modules\";
          version = \"0\";
          src = $root/homies/shimmerjs/home/khudson/$mod;
          vendorHash = \"sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\";
        }"
        echo "== $mod ==" >&2
        got=$(nix build --impure --no-link \
          --expr "($expr).goModules" 2>&1 \
          | grep -oE 'got: +sha256-[A-Za-z0-9+/=]+' \
          | sed -E 's/got: +//' || true)
        if [ -z "$got" ]; then
          echo "$mod: FAILED to extract hash -- full output:" >&2
          nix build --impure --no-link --expr "($expr).goModules" >&2 || true
          return 1
        fi
        printf '%s\t%s\n' "$mod" "$got"
      }

      recompute khudson
      recompute touchd
    '';
  };

  tasks = [
    khudsonTest
    khudsonRace
    khudsonBuild
    khudsonVet
    khudsonLive
    khudsonVendorhash
  ];
in
pkgs.mkShell {
  packages = [
    pkgs.go
    pkgs.gopls
    # runtime tools the modules/tests exec that are NOT macOS builtins
    pkgs.m1ddc
    pkgs.gh
    pkgs.kitty # provides `kitten`
    pkgs.btop
    pkgs.sqlite # provides `sqlite3`
  ]
  ++ tasks;

  shellHook = ''
    export KHUDSON_MODULE_ROOT="$(git rev-parse --show-toplevel 2>/dev/null)/homies/shimmerjs/home/khudson/khudson"
    cat <<'EOF'
    khudson dev shell. Module: homies/shimmerjs/home/khudson/khudson
      khudson-test        go test ./...          (host tools present -> green)
      khudson-race        go test -race          (./internal/bus ./internal/dock)
      khudson-build       go build -> /tmp/khudson/khudson
      khudson-vet         build + go vet ./...
      khudson-live        env-gated live tests   (KHUDSON_KEYMAPP_DB/CLAUDE_LIVE/SPIKE1/AX)
      khudson-vendorhash  recompute khudson + touchd vendorHash (after a dep bump)
    cd "$KHUDSON_MODULE_ROOT" to work in the khudson module.
    EOF
  '';
}
