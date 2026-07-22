# Flake check: the kitty version pin. Every kitty RC claim in khudson --
# ls JSON shape, focus-window/launch semantics, user-var survival, and now
# the reno demo verbs riding the same seam -- is verified against exactly
# one kitty version, pinned in internal/rc/rc.go (rc.Version). kitten
# resolves from the floating per-user profile PATH at exec time, so a
# nixpkgs bump that moves kitty would silently invalidate the pin and can
# stall a demo live on stage (wait guards + focus confirmation read ls).
# This check fails the closure instead: a deliberate kitty bump means
# re-verifying the claim set and moving the Go pin in the same commit.
{ pkgs }:
let
  rcGo = ../khudson/internal/rc/rc.go;
in
pkgs.runCommand "khudson-kitty-pin-check" { } ''
  pin=$(grep -Eo 'var Version = \[3\]int\{[0-9]+, [0-9]+, [0-9]+\}' ${rcGo} \
    | grep -Eo '[0-9]+, [0-9]+, [0-9]+' | tr -d ' ' | tr ',' '.')
  if [ -z "$pin" ]; then
    echo "kitty-pin-check: cannot find rc.Version in internal/rc/rc.go" >&2
    exit 1
  fi
  actual=${pkgs.kitty.version}
  if [ "$pin" != "$actual" ]; then
    echo "kitty-pin-check: nixpkgs kitty is $actual but internal/rc/rc.go pins $pin" >&2
    echo "a kitty bump must re-verify the RC claim set (ls shape, focus/launch," >&2
    echo "user vars, reno verbs) and move the Go pin in the same commit" >&2
    exit 1
  fi
  echo "kitty $actual matches the rc.go pin" > $out
''
