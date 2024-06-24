# Hack utility functions.

NIX_VERSION="2.22.1"

HOSTNAME="${HOSTNAME:-$(hostname)}"
UNIVERSE_PATH="${UNIVERSE_PATH:-$(dirname ${BASH_SOURCE[0]/..})}"

DISTDIR="dist"
RESULTDIR="result"

set -eu

# Return value based on OS. If Linux, return first argument, if 
# macOS, return second argument.
os_switch() {
  if [[ "$OSTYPE" == "linux-gnu"* ]]; then
    echo "$1"
  elif [[ "$OSTYPE" == "darwin"* ]]; then
    echo "$2"
  else
    echo "tried to figure out OS, and man, like, idk dude"
    exit 1
  fi
}