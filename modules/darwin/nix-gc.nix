# Weekly GC for macOS hosts, split into scripts so the load-bearing filter is
# testable on its own (lib/nix-gc-check.nix). Imported by ./default.nix.
#
# Two entry points over one filter: `nix-gc-weekly` (the launchd daemon, keeps
# the build graph) and `nix-gc-force` (manual, drops it plus the dev caches).
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
  # `nix-gc-force` default retention. Deliberately tighter than the weekly
  # window: a force run is asked for when the disk is full, and the generations
  # are the largest thing a prune can reach. Overridable with --window.
  forceWindow ? "1d",
  # Cache trees the force run sweeps after the collect. Globbed per user so an
  # entry covers every account on the host. Everything here is regenerated on
  # demand by the tool that owns it -- nothing unique lives in these.
  forceCaches ? [
    "/Users/*/Library/Caches/go-build" # go rebuilds it on the next compile
    "/Users/*/.cache/twine.pre-flat-*" # snapshots from the twine flattening
    "/Users/*/Library/Caches/com.tinyspeck.slackmacgap.ShipIt" # Slack updater staging
  ],
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
        # A .drv is a regular file, so it cannot carry a bundle directory.
        # Worth special-casing rather than letting find prove it: derivations
        # are 79% of a force run's dead list (60k of 76k here), and this skips
        # that many process spawns. The -d guard keeps the shortcut honest.
        if [ ! -d "$p" ]; then
          case "$p" in
            *.drv)
              printf '%s\n' "$p" >> "$collect"
              continue
              ;;
          esac
        fi
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

  # Manual counterpart to the weekly job, for when the disk is actually full.
  # Same .app hazard filter, three differences:
  #
  #   1. It enumerates dead paths with keep-outputs/keep-derivations OFF, so the
  #      build graph counts as garbage. Those hops (modules/nix.nix) are what
  #      pin the bulk of this store -- 88 of 102 GiB here on 2026-08-20 -- and
  #      the weekly job deliberately leaves them alone. The cost is real: every
  #      .drv and build-only output goes, so the next rebuild of anything the
  #      binary cache does not have builds from source.
  #   2. Tighter default generation window, and it sweeps the dev caches too.
  #   3. Reports and exits unless given --yes. A dry run touches nothing, not
  #      even generations, so the numbers can be read before committing.
  #
  # Paths go to the delete over --stdin, not argv: the force list runs to tens
  # of thousands of paths (76k here), which is several MB of argv against a 1 MB
  # kern.argmax. One invocation either way, for the reason the weekly job gives.
  force = pkgs.writeShellApplication {
    name = "nix-gc-force";
    runtimeInputs = with pkgs; [
      coreutils
      gnugrep
      gnused
    ];
    text = ''
      argv=("$@")
      window=${forceWindow}
      apply=0
      do_gc=1
      do_caches=1
      while [ "$#" -gt 0 ]; do
        case "$1" in
          -y | --yes) apply=1 ;;
          --window)
            window=''${2:?--window needs a value like 1d}
            shift
            ;;
          --skip-gc) do_gc=0 ;;
          --skip-caches) do_caches=0 ;;
          -h | --help)
            echo "usage: nix-gc-force [-y|--yes] [--window Nd] [--skip-gc] [--skip-caches]"
            echo "  reports what it would free unless --yes is given"
            exit 0
            ;;
          *)
            echo "nix-gc-force: unknown argument '$1'" >&2
            exit 2
            ;;
        esac
        shift
      done

      # Only the apply path needs root; enumerating, du and the cache sizes all
      # read fine as the caller, so the default report stays password-free.
      if [ "$apply" -eq 1 ] && [ "$(id -u)" -ne 0 ]; then
        exec sudo -- "$0" "''${argv[@]}"
      fi

      nixbin=${nixPackage}/bin
      say() { echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) nix-gc-force: $*"; }

      # Same reason as the weekly script: sudo leaks the caller's HOME and every
      # nix call then warns it is falling back to passwd's. Only under root --
      # an unprivileged report keeps its own HOME, which is where nix finds the
      # user profile roots.
      if [ "$(id -u)" -eq 0 ]; then unset HOME; fi

      # du in ONE process per set. optimise.automatic hardlinks identical files
      # across paths, so a chunked walk counts the shared inodes once per chunk
      # and inflates the total; a single walk is the on-disk number. Still an
      # upper bound on what a delete frees, since a file hardlinked to a live
      # path survives.
      disk_kb() {
        local kb=""
        if [ -s "$1" ]; then
          kb=$(tr '\n' '\0' < "$1" | du -sk --files0-from=- -c 2>/dev/null | tail -1 | cut -f1)
        fi
        echo "''${kb:-0}"
      }
      gib() { printf '%d.%01d GiB' "$(($1 / 1048576))" "$((($1 % 1048576) * 10 / 1048576))"; }

      if [ "$apply" -eq 0 ]; then
        say "DRY RUN (--yes to apply): window=$window gc=$do_gc caches=$do_caches"
      else
        say "APPLYING: window=$window gc=$do_gc caches=$do_caches"
      fi

      if [ "$do_gc" -eq 1 ]; then
        # Generations are live roots; without this prune the collect has little
        # to free. Absolute-path glob over the user profiles for the same reason
        # the weekly job does it: root's $HOME hides them from nix's discovery.
        # nix never deletes a profile's current generation, so this cannot strip
        # a rollback target that is actually in use.
        if [ "$apply" -eq 1 ]; then
          say "pruning system profile generations older than $window"
          "$nixbin"/nix-env --profile /nix/var/nix/profiles/system --delete-generations "$window"
          shopt -s nullglob
          for p in /Users/*/.local/state/nix/profiles/* /nix/var/nix/profiles/per-user/*/*; do
            case "$p" in *-link) continue ;; esac
            [ -L "$p" ] || continue
            say "pruning $p generations older than $window"
            "$nixbin"/nix-env --profile "$p" --delete-generations "$window" || say "  prune failed, continuing"
          done
          shopt -u nullglob
        else
          say "would prune system + user profile generations older than $window"
        fi

        work=$(mktemp -d)
        trap 'rm -rf "$work"' EXIT

        say "enumerating dead paths with keep-outputs/keep-derivations OFF (minutes on a large store)"
        if ! "$nixbin"/nix-store --gc --print-dead \
          --option keep-outputs false --option keep-derivations false \
          > "$work/raw" 2> "$work/err"; then
          say "print-dead failed:"
          sed 's/^/  /' "$work/err" >&2
          exit 1
        fi
        grep '^/nix/store/' "$work/raw" > "$work/dead" || true

        ${partition}/bin/nix-gc-partition "$work/dead" "$work/held" "$work/raw-collect"

        # Held-back bundles' own dead dependencies have to leave the list too:
        # nix-store --delete walks referrers, and the walk reaching a path
        # outside the list reports the listed path as still alive. See the
        # weekly script for the long version.
        : > "$work/hazard-closure"
        while IFS= read -r h; do
          printf '%s\n' "$h" >> "$work/hazard-closure"
          "$nixbin"/nix-store -q --requisites "$h" >> "$work/hazard-closure" 2>/dev/null || true
        done < "$work/held"
        sort -u "$work/hazard-closure" -o "$work/hazard-closure"
        sort -u "$work/raw-collect" -o "$work/raw-collect"
        comm -23 "$work/raw-collect" "$work/hazard-closure" > "$work/collect"

        ndead=$(wc -l < "$work/dead" | tr -d ' ')
        nheld=$(wc -l < "$work/held" | tr -d ' ')
        ncollect=$(wc -l < "$work/collect" | tr -d ' ')
        say "dead=$ndead collectible=$ncollect retained=$((ndead - ncollect)) (bundles=$nheld + their dead deps)"
        if [ "$nheld" -gt 0 ]; then
          say "held back, App Management TCC would abort the sweep on these:"
          sed 's/^/  /' "$work/held"
        fi
        say "collectible on disk: $(gib "$(disk_kb "$work/collect")")"

        if [ "$ncollect" -eq 0 ]; then
          say "nothing to collect"
        elif [ "$apply" -eq 0 ]; then
          # An understatement, and not by a little: the generations are still
          # live roots here, so everything only they pin still counts as live.
          say "would delete $ncollect paths -- a floor, the prune this run skipped frees more"
        else
          attempt=0
          while :; do
            attempt=$((attempt + 1))
            if "$nixbin"/nix-store --delete --stdin \
              --option keep-outputs false --option keep-derivations false \
              < "$work/collect" > "$work/out" 2> "$work/err"; then
              # The no-op success is the failure mode worth a hard check:
              # `nix-store --delete` given no path ARGUMENTS ignores stdin
              # entirely, deletes nothing, runs the .links sweep and exits 0 --
              # indistinguishable from a clean run in the log. Probe the store
              # rather than parse nix's summary line, which is a message format.
              probe=$(head -1 "$work/collect")
              if [ -e "$probe" ] || [ -L "$probe" ]; then
                say "delete exited 0 but $probe is still in the store -- the path list did not reach nix"
                sed 's/^/  /' "$work/out" >&2
                exit 1
              fi
              say "collected $ncollect paths (attempt $attempt)"
              grep -E '^[0-9]+ store paths deleted' "$work/out" "$work/err" | sed 's/^/  /' || true
              break
            fi
            raced=$(sed -n "s/.*Cannot delete path '\([^']*\)'.*/\1/p" "$work/err" | sort -u)
            if [ -z "$raced" ] || [ "$attempt" -ge 8 ]; then
              say "nix-store --delete failed on attempt $attempt; nix said:"
              sed 's/^/  /' "$work/err" >&2
              exit 1
            fi
            # Drop a raced path WITH its closure: left behind, it is an
            # outside-the-list referrer for everything it depends on.
            : > "$work/drop"
            while IFS= read -r r; do
              printf '%s\n' "$r" >> "$work/drop"
              "$nixbin"/nix-store -q --requisites "$r" >> "$work/drop" 2>/dev/null || true
            done <<< "$raced"
            sort -u "$work/drop" -o "$work/drop"
            comm -23 "$work/collect" "$work/drop" > "$work/collect.next"
            say "dropped $(comm -12 "$work/collect" "$work/drop" | wc -l | tr -d ' ') raced paths and their closures, retrying"
            mv "$work/collect.next" "$work/collect"
          done
        fi
      fi

      if [ "$do_caches" -eq 1 ]; then
        shopt -s nullglob
        for c in ${lib.concatStringsSep " " forceCaches}; do
          [ -e "$c" ] || continue
          sz=$(du -sk "$c" 2>/dev/null | cut -f1)
          sz=''${sz:-0}
          if [ "$apply" -eq 1 ]; then
            say "removing $c ($(gib "$sz"))"
            rm -rf "$c"
          else
            say "would remove $c ($(gib "$sz"))"
          fi
        done
        shopt -u nullglob
      fi

      say "df:"
      df -h / | sed 's/^/  /'
    '';
  };
in
{
  inherit
    partition
    collect
    force
    logFile
    ;
}
