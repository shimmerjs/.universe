# PostToolUse(Bash): after a `go build`, move any stray compiled binary that landed
# in the source tree into a gitignored .claude/bin/ so it cannot be committed.
# Non-blocking; reports what it moved. Binaries (git, file, jq) come from nix.

input=$(cat)
cmd=$(jq -r '.tool_input.command // empty' <<<"$input")
[[ "$cmd" == *"go build"* ]] || exit 0

cwd=$(jq -r '.cwd // empty' <<<"$input")
[[ -n "$cwd" && -d "$cwd" ]] || cwd="$PWD"
root=$(cd "$cwd" && git rev-parse --show-toplevel 2>/dev/null) || exit 0

bindir="$root/.claude/bin"
moved=()
while IFS= read -r -d '' rel; do
  f="$root/$rel"
  [[ -f "$f" && -x "$f" ]] || continue
  case "$(file -b "$f" 2>/dev/null)" in
    *Mach-O*executable*|*ELF*executable*) ;;
    *) continue ;;
  esac
  mkdir -p "$bindir"
  mv -f "$f" "$bindir/"
  moved+=("$rel")
done < <(cd "$root" && git ls-files --others --exclude-standard -z)

[[ ${#moved[@]} -eq 0 ]] && exit 0
{
  echo "clod: moved stray go binary out of the source tree into .claude/bin/ (gitignored):"
  printf '  - %s\n' "${moved[@]}"
} >&2
exit 0
