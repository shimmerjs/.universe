# Command boundaries are functions so the Nix check can replace them.
khudsonLaunchctl() { /bin/launchctl "$@"; }
khudsonOwnerAlive() { /bin/kill -0 "$1" 2>/dev/null; }
khudsonOwnerWait() { /bin/sleep 0.2; }

khudsonRetireOwner() {
  [ -n "${DRY_RUN:-}" ] && return 0
  local service="gui/$khudsonAgentsUid/$1" state ownerPid attempts
  if state=$(khudsonLaunchctl print "$service" 2>/dev/null); then
    ownerPid=$(printf '%s\n' "$state" | awk '$1 == "pid" && $2 == "=" { print $3; exit }')
    khudsonLaunchctl bootout "$service" || return 1
    attempts=50
    while [ -n "$ownerPid" ] && khudsonOwnerAlive "$ownerPid"; do
      if [ "$attempts" -eq 0 ]; then
        echo "khudson: owner $1 pid $ownerPid did not exit; refusing replacement" >&2
        return 1
      fi
      khudsonOwnerWait
      attempts=$((attempts - 1))
    done
  fi
  return 0
}
