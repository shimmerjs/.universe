# PostToolUse(Edit|Write|MultiEdit): per-edit gofmt syntax gate + goimports.
# Queues the edited file for the Stop hook's batched build/vet pass.
# Binaries (go/gofmt, goimports, jq) come from nix via writeShellApplication.
# Runs under `set -euo pipefail` (writeShellApplication adds it + the shebang).

input=$(cat)
file=$(jq -r '.tool_input.file_path // empty' <<<"$input")
session=$(jq -r '.session_id // empty' <<<"$input")

if [[ -z "$file" || "$file" != *.go || ! -f "$file" || -z "$session" ]]; then
  exit 0
fi

# Defer the expensive build/vet to the Stop hook; just record the path.
# CLOD_GO_PENDING_DIR: hermetic-check override, matches gocheck's reader.
printf '%s\n' "$file" >> "${CLOD_GO_PENDING_DIR:-/tmp}/go-pending-${session}"

# Syntax gate: gofmt -e prints parse errors to stderr. Block the edit on any.
err=$(gofmt -e "$file" 2>&1 >/dev/null) || true
if [[ -n "$err" ]]; then
  base=$(basename "$file")
  pat="$file:"
  rep="$base:"
  printf '%s\n' "${err//"$pat"/"$rep"}" >&2
  exit 2
fi

# Format + fix imports in place. Cheap; safe to run every edit.
goimports -w "$file" 2>/dev/null || true
