# PreToolUse(Workflow): deny name= invocations that resolve to a deployed
# ~/.claude/workflows script. The Workflow name registry is frozen at session
# start (files added mid-session are invisible to it), so name= can run a
# stale pre-switch script; scriptPath reads the file at invocation, so it is
# always the currently deployed artifact. Built-ins and plugin workflows have
# no deployed file and pass through.
# Runs under `set -euo pipefail` (writeShellApplication adds it + the shebang).

input=$(cat)
name=$(jq -r '.tool_input.name // empty' <<<"$input")
scriptpath=$(jq -r '.tool_input.scriptPath // empty' <<<"$input")

# scriptPath outranks name inside the tool itself; nothing to gate.
if [[ -n "$scriptpath" || -z "$name" ]]; then
  exit 0
fi
# a name with a path separator can't be a deployed-workflow name
case "$name" in */*) exit 0 ;; esac

deployed="$HOME/.claude/workflows/${name}.js"
if [[ ! -e "$deployed" ]]; then
  exit 0
fi

{
  echo "Workflow name= resolution is frozen at session start and can run a stale pre-switch script."
  echo "Re-invoke this workflow via its deployed path (read at invocation time), same args:"
  echo "  Workflow({scriptPath: \"$deployed\", ...})"
} >&2
exit 2
