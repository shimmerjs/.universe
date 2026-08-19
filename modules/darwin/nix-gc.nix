# Weekly GC for macOS hosts, split into two scripts so the load-bearing filter
# is testable on its own (lib/nix-gc-check.nix). Imported by ./default.nix.
#
# Why we don't use nix-darwin's nix.gc: `nix-collect-garbage` aborts the WHOLE
# run with `fchmodat ... Operation not permitted` the moment it reaches a dead
# `.app` bundle, because the deleting process lacks the macOS App Management
# TCC grant. The abort happens mid-sweep, so every path after it in deletion
# order is skipped too, and the final unused-.links sweep never runs -- the job
# frees nothing and says nothing (neither builtin gc plist sets
# StandardOutPath). VS Code is the recurring source: each bump can leave another
# dead bundle behind, and one is enough to wedge the sweep.
#
# So: enumerate dead paths, hold back the ones that carry a real .app bundle
# plus everything those depend on, delete the rest in one call.
{
  lib,
  pkgs,
  nixPackage,
  # mirrors nix.gc.options in ../nix.nix
  window ? "14d",
  logFile ? "/var/log/nix-gc.log",
}:
let
  windowDays = lib.removeSuffix "d" window;
  # Partitions a list of dead store paths into "would wedge the sweep" and
  # "safe to delete". Standalone (reads a file, writes two) so the check can
  # drive it over a fixture tree.
  #
  # The distinction that makes or breaks this: `-type d`. home-manager-path,
  # home-manager-applications and every darwin-system generation carry an
  # Applications/ directory full of SYMLINKS to bundles, and those generations
  # are exactly the garbage most worth collecting. Only a real bundle directory
  # is a hazard; a symlink to one deletes fine. Depth is unbounded (the two
  # known hazards sit at Applications/<name>.app, but a bundle anywhere in the
  # closure aborts the same way) and `-print -quit` stops the walk at the first
  # hit.
  partition = pkgs.writeShellApplication {
    name = "nix-gc-partition";
    runtimeInputs = with pkgs; [
      coreutils
      findutils
    ];
    text = ''
      if [ "$#" -ne 3 ]; then
        echo "usage: nix-gc-partition <dead-list> <held-back-out> <collectible-out>" >&2
        exit 2
      fi
      dead=$1 held=$2 collect=$3
      : > "$held"
      : > "$collect"

      while IFS= read -r p; do
        [ -n "$p" ] || continue
        bundle=$(find "$p" -type d -name '*.app' -print -quit 2>/dev/null || true)
        if [ -n "$bundle" ]; then
          printf '%s\n' "$p" >> "$held"
        else
          printf '%s\n' "$p" >> "$collect"
        fi
      done < "$dead"
    '';
  };

  collect = pkgs.writeShellApplication {
    name = "nix-gc-weekly";
    runtimeInputs = with pkgs; [
      coreutils # date/stat are GNU here, not BSD: stat -c, and lstat by default
      gnugrep
      gnused
    ];
    text = ''
      nixbin=${nixPackage}/bin
      say() { echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) nix-gc: $*"; }

      # launchd hands a daemon no HOME, but a manual `sudo` run leaks the
      # invoking user's, and every nix call then warns that it is falling back to
      # passwd's. Drop it: same environment either way, and the log stays readable.
      unset HOME

      # The nix-darwin `system` profile needs an explicit prune: every old
      # generation is a live GC root pinning its whole closure, so without this
      # the collect below has nothing to free. Prune first, collect second, same
      # run. Always keeps the current generation.
      say "pruning system profile generations older than ${window}"
      "$nixbin"/nix-env --profile /nix/var/nix/profiles/system --delete-generations ${window}

      # Root's $HOME is /var/root, and nix discovers user profiles THROUGH $HOME
      # ($XDG_STATE_HOME/nix/profiles), so a root collect is structurally blind to
      # /Users/*/.local/state/nix/profiles -- NixOS/nix#8508, and the second half
      # of why this store never shrank. Those profiles are live GC roots: one
      # standalone-home-manager generation left behind in Jun 2025 pinned 479
      # paths / 6.5 GiB here. Reach them by absolute path.
      #
      # Discovered by glob rather than from the nix users we already know at eval
      # time: this has to cover profiles nothing in the config created (root's
      # per-user channels, leftovers from a previous topology) -- exactly the
      # class that went unnoticed.
      #
      # A generation prune cannot help when the stale generation is the profile's
      # CURRENT one (nix never deletes current), which is the shape that hid here
      # for 14 months -- so report that and leave it alone rather than delete
      # someone's live profile behind their back.
      cutoff=$(($(date +%s) - ${windowDays} * 86400))
      shopt -s nullglob
      for p in /Users/*/.local/state/nix/profiles/* /nix/var/nix/profiles/per-user/*/*; do
        case "$p" in *-link) continue ;; esac
        [ -L "$p" ] || continue
        say "pruning $p generations older than ${window}"
        "$nixbin"/nix-env --profile "$p" --delete-generations ${window} || say "  prune failed, continuing"
        mtime=$(stat -c %Y "$p" 2>/dev/null || echo 0)
        if [ "$mtime" -lt "$cutoff" ]; then
          say "  STALE: current generation is $(((cutoff + ${windowDays} * 86400 - mtime) / 86400))d old and still pins its whole closure; rm the profile if it is dead"
        fi
      done
      shopt -u nullglob

      work=$(mktemp -d)
      trap 'rm -rf "$work"' EXIT

      say "enumerating dead paths (minutes on a large store)"
      if ! "$nixbin"/nix-store --gc --print-dead > "$work/raw" 2> "$work/err"; then
        say "print-dead failed:"
        sed 's/^/  /' "$work/err" >&2
        exit 1
      fi
      grep '^/nix/store/' "$work/raw" > "$work/dead" || true

      ${partition}/bin/nix-gc-partition "$work/dead" "$work/held" "$work/raw-collect"

      # Holding a path back is not enough: nix-store --delete walks the REFERRER
      # closure of every path it is handed, and the moment that walk reaches a
      # path outside the list it gives up and reports the listed path as still
      # alive (libstore gc.cc, the gcDeleteSpecific arm). So every dead path a
      # held-back bundle depends on has to come out of the list too -- p can
      # reach hazard h iff p is in requisites(h). Deleting with the keep-outputs
      # / keep-derivations hops off (they are on globally, see modules/nix.nix)
      # keeps the walk to referrers alone, which is what makes requisites the
      # exact and complete exclusion instead of a guess. The dead list itself
      # was enumerated WITH the hops on, so it stays conservative either way.
      : > "$work/hazard-closure"
      while IFS= read -r h; do
        printf '%s\n' "$h" >> "$work/hazard-closure"
        # invalid paths (half-deleted carcasses from earlier aborted sweeps)
        # have no closure to query and no dependents to protect
        "$nixbin"/nix-store -q --requisites "$h" >> "$work/hazard-closure" 2>/dev/null || true
      done < "$work/held"
      sort -u "$work/hazard-closure" -o "$work/hazard-closure"
      sort -u "$work/raw-collect" -o "$work/raw-collect"
      comm -23 "$work/raw-collect" "$work/hazard-closure" > "$work/collect"

      ndead=$(wc -l < "$work/dead" | tr -d ' ')
      nheld=$(wc -l < "$work/held" | tr -d ' ')
      ncollect=$(wc -l < "$work/collect" | tr -d ' ')
      nretained=$((ndead - ncollect))
      say "dead=$ndead collectible=$ncollect retained=$nretained (bundles=$nheld + their dead deps)"
      if [ "$nheld" -gt 0 ]; then
        say "held back, App Management TCC would abort the sweep on these:"
        sed 's/^/  /' "$work/held"
      fi

      if [ "$ncollect" -eq 0 ]; then
        say "nothing to collect"
        exit 0
      fi

      # One invocation: every nix-store --delete does a full GC-root trace, so
      # per-path deletion pays it N times (hours, measured on this store) and
      # batching fails wholesale on cross-batch referrers. Store paths cannot
      # contain whitespace, so splitting the list on it is safe.
      #
      # Enumerate and delete are two commands under two separate GC locks, so a
      # build landing in between can give a listed path a live referrer, and one
      # such path aborts the whole invocation ("Cannot delete path ... since it
      # is still alive"). nix names it, so drop it and retry rather than lose the
      # run to a race -- but drop its requisites with it. A dropped path is an
      # outside-the-list referrer for everything it depends on, so dropping it
      # alone re-creates the wedge the hazard filter exists to avoid, one path
      # per attempt, and the retries chase their own tail. The cap bounds a
      # genuinely stuck list; each attempt pays another root trace.
      attempt=0
      while :; do
        attempt=$((attempt + 1))
        # shellcheck disable=SC2046
        if "$nixbin"/nix-store --delete \
          --option keep-outputs false --option keep-derivations false \
          $(tr '\n' ' ' < "$work/collect") > "$work/out" 2> "$work/err"; then
          say "collected $(wc -l < "$work/collect" | tr -d ' ') paths (attempt $attempt)"
          grep -E '^[0-9]+ store paths deleted' "$work/out" | sed 's/^/  /' || true
          break
        fi
        raced=$(sed -n "s/.*Cannot delete path '\([^']*\)'.*/\1/p" "$work/err" | sort -u)
        if [ -z "$raced" ] || [ "$attempt" -ge 8 ]; then
          say "nix-store --delete failed on attempt $attempt; nix said:"
          sed 's/^/  /' "$work/err" >&2
          exit 1
        fi
        : > "$work/drop"
        while IFS= read -r r; do
          printf '%s\n' "$r" >> "$work/drop"
          "$nixbin"/nix-store -q --requisites "$r" >> "$work/drop" 2>/dev/null || true
        done <<< "$raced"
        sort -u "$work/drop" -o "$work/drop"
        comm -23 "$work/collect" "$work/drop" > "$work/collect.next"
        printf '%s\n' "$raced" | sed 's/^/  went live since enumerate, dropping with its closure: /'
        say "dropped $(comm -12 "$work/collect" "$work/drop" | wc -l | tr -d ' ') paths, retrying"
        mv "$work/collect.next" "$work/collect"
      done
    '';
  };
in
{
  inherit partition collect logFile;
}
