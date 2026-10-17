set -eu
source "$1"
khudsonAgentsUid=501
DRY_RUN=
khudsonLaunchctl() {
  case "$1" in
    print) [ "$loaded" = yes ] || return 1; printf 'pid = 42\n' ;;
    bootout) bootouts=$((bootouts + 1)); [ "$bootoutOK" = yes ] ;;
    *) echo "unexpected launchctl operation: $*" >&2; return 1 ;;
  esac
}
khudsonOwnerAlive() { [ "$alive" -gt 0 ]; }
khudsonOwnerWait() { waits=$((waits + 1)); alive=$((alive - 1)); }
reset() { loaded=yes; bootoutOK=yes; bootouts=0; waits=0; alive=2; }
reset
khudsonRetireOwner org.khudson.bus
[ "$bootouts" -eq 1 ] && [ "$waits" -eq 2 ]
reset
loaded=no
khudsonRetireOwner org.khudson.bus
[ "$bootouts" -eq 0 ] && [ "$waits" -eq 0 ]
reset
bootoutOK=no
if khudsonRetireOwner org.khudson.bus; then exit 1; fi
[ "$waits" -eq 0 ]
reset
alive=100
if khudsonRetireOwner org.khudson.bus; then exit 1; fi
[ "$waits" -eq 50 ]
reset
DRY_RUN=1
khudsonRetireOwner org.khudson.bus
[ "$bootouts" -eq 0 ] && [ "$waits" -eq 0 ]
printf 'five owner-retirement scenarios passed\n'
