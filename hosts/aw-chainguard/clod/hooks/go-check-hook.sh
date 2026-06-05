# Stop hook: drain the .go edits queued this session, batch them by module root
# (go.work wins over the nearest go.mod), and run `go build` then `go vet` on the
# edited packages. Compiler + vet is the ground truth; no LSP pass. Run from the
# module root with relative package args so go emits repo-relative diagnostics.
# Binaries (go, jq) come from nix via writeShellApplication.
# Runs under `set -euo pipefail`.

input=$(cat)
session=$(jq -r '.session_id // empty' <<<"$input")
[[ -z "$session" ]] && exit 0

pending="/tmp/go-pending-${session}"
[[ -f "$pending" ]] || exit 0

# Avoid a re-fire loop: if this Stop already blocked once this cycle, drain the
# queue and let the turn end (the error was surfaced; do not block forever).
[[ "$(jq -r '.stop_hook_active // false' <<<"$input")" == "true" ]] && { rm -f "$pending"; exit 0; }

mapfile -t files < <(sort -u "$pending")

find_go_root() {
  local dir="$1" mod=""
  while [[ "$dir" == /* && "$dir" != "/" ]]; do
    [[ -z "$mod" && -f "$dir/go.mod" ]] && mod="$dir"
    if [[ -f "$dir/go.work" ]]; then
      printf '%s\n' "$dir"
      return 0
    fi
    dir=$(dirname "$dir")
  done
  printf '%s\n' "$mod"
}

declare -A pkgs_by_root=()
for f in "${files[@]}"; do
  [[ -z "$f" || ! -f "$f" ]] && continue
  root=$(find_go_root "$(dirname "$f")")
  [[ -z "$root" ]] && continue
  rel="${f#"$root"/}"
  pkgs_by_root["$root"]+="./$(dirname "$rel")"$'\n'
done

output=""
for root in "${!pkgs_by_root[@]}"; do
  mapfile -t pkgs < <(printf '%s' "${pkgs_by_root[$root]}" | sort -u)
  [[ ${#pkgs[@]} -eq 0 ]] && continue

  # build first: stricter ground truth than vet (catches build-tag/cgo breaks
  # vet would skip). Build into a throwaway dir (-o "$out/") so a `main` package
  # never drops a binary into the source tree. vet adds signal once it compiles.
  out=$(mktemp -d)
  build=$(cd "$root" && timeout 60 go build -o "$out/" "${pkgs[@]}" 2>&1) || true
  rm -rf "$out"
  if [[ -n "$build" ]]; then
    output+="$build"$'\n'
    continue
  fi
  vet=$(cd "$root" && timeout 30 go vet "${pkgs[@]}" 2>&1) || true
  [[ -n "$vet" ]] && output+="$vet"$'\n'
done

# clean: drain the queue and pass. On failure the queue is KEPT, so the gate
# persists even if the next stop doesn't touch a new .go file.
if [[ -z "${output//[[:space:]]/}" ]]; then rm -f "$pending"; exit 0; fi

# failure: hard-gate the turn end and hand the model the diagnostic. Stop hooks
# (CC >= 2.1.163) carry hookSpecificOutput.additionalContext, so deliver the full
# build/vet output as structured context instead of raw stderr; keep decision=block
# for the gate, with the first error line in the reason so it stays visible regardless.
first="${output%%$'\n'*}"
jq -nc --arg out "$output" --arg first "$first" '{
  decision: "block",
  reason: ("go build/vet failed on packages edited this turn -- fix before ending the turn. First error: " + $first + " (full output in additionalContext)."),
  hookSpecificOutput: { hookEventName: "Stop", additionalContext: $out }
}'
exit 0
