# PostToolUse(Edit|Write|MultiEdit|apply_patch): per-edit gofmt syntax gate +
# goimports. Queues the edited files for the Stop hook's batched build/vet pass.
# Binaries (go/gofmt, goimports, jq) come from nix via writeShellApplication.
# Runs under `set -euo pipefail` (writeShellApplication adds it + the shebang).
#
# Two payload shapes, one hook. clod's Write/Edit/MultiEdit carry a single
# tool_input.file_path; cody's (codex) apply_patch carries a whole patch
# envelope in tool_input.command, which may touch several files at once. Both
# reduce to a list of paths that already exist on disk -- PostToolUse fires
# after the write, and only on success.

input=$(cat)
session=$(jq -r '.session_id // empty' <<<"$input")
[[ -n "$session" ]] || exit 0

# file_path when present, otherwise every file the patch envelope names.
# `*** Move to:` is the rename destination, which is the file that now exists.
mapfile -t files < <(jq -r '
  if (.tool_input.file_path // "") != "" then .tool_input.file_path
  else
    (.tool_input.command // "")
    | split("\n")[]
    | capture("^\\*\\*\\* (?:Add File|Update File|Move to): (?<p>.+)$")
    | .p
  end
' <<<"$input")

rc=0
errs=""
for file in "${files[@]}"; do
  [[ "$file" == *.go && -f "$file" ]] || continue

  # Defer the expensive build/vet to the Stop hook; just record the path.
  # CLOD_GO_PENDING_DIR: hermetic-check override, matches gocheck's reader.
  printf '%s\n' "$file" >> "${CLOD_GO_PENDING_DIR:-/tmp}/go-pending-${session}"

  # Syntax gate: gofmt -e prints parse errors to stderr. Block the edit on any.
  err=$(gofmt -e "$file" 2>&1 >/dev/null) || true
  if [[ -n "$err" ]]; then
    base=$(basename "$file")
    pat="$file:"
    rep="$base:"
    errs+="${err//"$pat"/"$rep"}"$'\n'
    rc=2
    continue
  fi

  # Format + fix imports in place. Cheap; safe to run every edit.
  goimports -w "$file" 2>/dev/null || true
done

if [[ "$rc" -ne 0 ]]; then
  printf '%s' "$errs" >&2
  exit 2
fi
