# PostToolUse(Bash): after a `go build`, move the stray binary THIS build dropped
# in the source tree into a gitignored .claude/bin/ so it can't be committed.
#
# Narrow on purpose: the candidate set is only the build's `-o` target, or (no -o)
# the untracked executables at the TOP of the build's working dir -- where a bare
# `go build` drops its default output. It is NOT a repo-wide sweep: an unrelated
# untracked binary elsewhere in the tree is left alone. Only untracked, non-ignored
# Mach-O/ELF executables are moved; a tracked or deliberately-ignored binary is
# never touched. Non-blocking; reports what it moved. Tools (git, file, jq) come
# from nix.

input=$(cat)
cmd=$(jq -r '.tool_input.command // empty' <<<"$input")
[[ "$cmd" == *"go build"* ]] || exit 0

cwd=$(jq -r '.cwd // empty' <<<"$input")
[[ -n "$cwd" && -d "$cwd" ]] || cwd="$PWD"
root=$(cd "$cwd" && git rev-parse --show-toplevel 2>/dev/null) || exit 0

# Candidates: only what THIS invocation could have produced.
candidates=()
if [[ "$cmd" =~ -o[[:space:]=]+([^[:space:]]+) ]]; then
  # explicit output target, resolved relative to the build cwd
  o="${BASH_REMATCH[1]}"
  [[ "$o" = /* ]] || o="$cwd/$o"
  candidates+=("$o")
else
  # default output lands in cwd, named after the package; only the build dir's top level
  shopt -s nullglob
  for f in "$cwd"/*; do
    [[ -f "$f" ]] && candidates+=("$f")
  done
  shopt -u nullglob
fi
[[ ${#candidates[@]} -eq 0 ]] && exit 0

bindir="$root/.claude/bin"
moved=()
for f in "${candidates[@]}"; do
  [[ -f "$f" && -x "$f" ]] || continue
  # an -o target outside the repo can't be committed here anyway
  case "$f" in "$root"/*) ;; *) continue ;; esac
  rel=${f#"$root"/}
  # untracked AND not ignored -- never relocate a tracked or intentionally-ignored binary
  git -C "$root" ls-files --others --exclude-standard -- "$rel" | grep -q . || continue
  case "$(file -b "$f" 2>/dev/null)" in
    *Mach-O*executable*|*ELF*executable*) ;;
    *) continue ;;
  esac
  mkdir -p "$bindir"
  mv -f "$f" "$bindir/"
  moved+=("$rel")
done

[[ ${#moved[@]} -eq 0 ]] && exit 0
{
  echo "clod: moved stray go binary out of the source tree into .claude/bin/ (gitignored):"
  printf '  - %s\n' "${moved[@]}"
} >&2
exit 0
