# SessionEnd: write a deterministic git snapshot to the OS temp dir as a fallback
# handoff breadcrumb, so even a session that ended without running /handoff leaves a
# recoverable pointer (branch, status, diffstat, transcript path).
#
# This captures ONLY what a shell can know. The semantic part of a handoff -- what is
# VERIFIED vs ASSUMED, the next action -- lives in the model's head, not on disk, so the
# model-authored `handoff` skill produces the rich version and overwrites this file.
# SessionEnd cannot add context or invoke the model (its stdout is debug-log only), so
# this is a pure side-effect breadcrumb, not a substitute for /handoff.
#
# Binaries (jq, git, coreutils) come from nix via writeShellApplication.
# Runs under `set -euo pipefail`; every external call is guarded so a dirty/borrowed
# repo or detached HEAD can never make SessionEnd error out.

input=$(cat)
session=$(jq -r '.session_id // empty' <<<"$input")
cwd=$(jq -r '.cwd // empty' <<<"$input")
transcript=$(jq -r '.transcript_path // empty' <<<"$input")
reason=$(jq -r '.reason // "other"' <<<"$input")
[[ -z "$session" ]] && exit 0

out="${TMPDIR:-/tmp}/clod-handoff-${session}.md"

# Only snapshot when ending inside a git repo with actual uncommitted work. A clean
# tree or a non-repo cwd has nothing worth a breadcrumb -- don't litter temp.
in_repo=$(cd "$cwd" 2>/dev/null && git rev-parse --is-inside-work-tree 2>/dev/null) || true
[[ "$in_repo" == "true" ]] || exit 0

status=$(cd "$cwd" && git status --porcelain 2>/dev/null) || true
[[ -z "$status" ]] && exit 0

# Don't clobber a richer model-authored handoff written this session by the skill.
# Heuristic: the skill emits a "## VERIFIED" section; this fallback never does.
if [[ -f "$out" ]] && grep -q '^## VERIFIED' "$out" 2>/dev/null; then
  exit 0
fi

branch=$(cd "$cwd" && git rev-parse --abbrev-ref HEAD 2>/dev/null) || branch="(detached)"
sha=$(cd "$cwd" && git rev-parse --short HEAD 2>/dev/null) || sha="(none)"
diffstat=$(cd "$cwd" && git diff --stat 2>/dev/null) || true

{
  printf '# Handoff (auto snapshot) -- %s -- %s\n\n' "$session" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'Session ended: %s. Deterministic git snapshot only -- no VERIFIED/ASSUMED\n' "$reason"
  printf 'split (a shell can'\''t know that). Run /handoff next time for the rich version.\n\n'
  printf '## State\n'
  printf -- '- Repo: %s\n' "$cwd"
  printf -- '- Branch: %s @ %s\n' "$branch" "$sha"
  [[ -n "$transcript" ]] && printf -- '- Transcript: %s\n' "$transcript"
  printf '\n## Uncommitted (git status --porcelain)\n```\n%s\n```\n' "$status"
  if [[ -n "$diffstat" ]]; then
    printf '\n## Diffstat (git diff --stat)\n```\n%s\n```\n' "$diffstat"
  fi
  printf '\n## Next action\nReview `git diff` in %s, then resume.\n' "$cwd"
} > "$out" 2>/dev/null || exit 0

exit 0
