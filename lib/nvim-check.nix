# Builds a derivation that starts nvim headless and fails if there are any
# lua or configuration errors on startup.  Used by flake `checks` so that
# `nix flake check` catches config regressions at build time.
{ pkgs, nvim }:

pkgs.runCommandLocal "nvim-startup-check" {
  nativeBuildInputs = [ nvim ];
} ''
  export HOME=$TMPDIR

  # Use a marker file instead of stdout — headless nvim prints lua output
  # to stderr, making stdout-based checks unreliable.
  nvim --headless \
    -c "lua io.open('$TMPDIR/ok', 'w'):close()" \
    -c 'qa' 2>$TMPDIR/stderr.log || true

  # Check for lua/config errors in stderr.
  if grep -qiE '^E[0-9]+:|Error executing lua|Failed to run' $TMPDIR/stderr.log; then
    echo "nvim startup check failed:" >&2
    cat $TMPDIR/stderr.log >&2
    exit 1
  fi

  # Verify lua actually executed (the marker file was created).
  if [ ! -f $TMPDIR/ok ]; then
    echo "nvim startup check failed: lua did not execute" >&2
    cat $TMPDIR/stderr.log >&2
    exit 1
  fi

  mkdir -p $out
  echo "nvim startup check passed" > $out/result
''
