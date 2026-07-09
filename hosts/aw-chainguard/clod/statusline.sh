#!/usr/bin/env bash
# claude-statusline.sh -- a flat (non-powerline) control-center statusline.
#                         Managed by ~/.universe.
#
# Tuned to how this account actually runs: heavy workflow/subagent fan-out, work
# spread across many repos and worktrunk worktrees. The model name always shows
# (the fabio/opie wink), effort shows only off-xhigh, location is repo-first, and
# the fleet is first-class.
#
# Layout (lines appear/disappear with state -- it grows when you're busy):
#   line 0  session codename   「name」          -- ONLY when Claude has named the chat
#   line 1  PLACE              ◆ model  repo ▸ subpath  ⎇ branch  PR  ctx gauge
#   line 2  CHURN              +added/-removed this session -- ONLY with real churn
#   line 3  FLEET              ⟳ N workflows · M agents live   -- ONLY while running
#
# LOCATION (the dedup): worktrunk names each worktree dir after its branch, so the
# worktree name carries no information the branch doesn't. The stable identity is
# the repo (workspace.repo.name, from origin -- survives across worktrees). So the
# line is repo ▸ subpath ⎇ branch: three orthogonal axes, no worktree badge.
#   repo    workspace.repo.name, else basename of the git toplevel, else cwd base
#   subpath cwd relative to the worktree root (omitted at root) -- the "where in the tree"
#   branch  current branch; emerald on trunk (worktrunk default-branch), amber @sha detached
#
# Everything renders straight from the payload plus a cheap fs scan for the live
# fleet row -- no transcript scanning, no background jobs, no cache.
#
# Tunables (env):
#   STATUSLINE_LIVEWIN   seconds an agent counts as "live" after last write (default 25)

export PATH="/etc/profiles/per-user/$USER/bin:/run/current-system/sw/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:$PATH"

JQ="$(command -v jq || true)"
LIVEWIN="${STATUSLINE_LIVEWIN:-25}"

input="$(cat)"

if [ -z "$JQ" ]; then
  printf 'claude -- install jq for the rich statusline\n'
  exit 0
fi

# ───────────────────────── payload ─────────────────────────
get(){ printf '%s' "$input" | "$JQ" -r "$1" 2>/dev/null; }
model_name="$(get '.model.display_name // .model.id // "Claude"')"
# lower kebab-case the model display name, with a wink: opus -> opie, fable -> fabio
model_disp="$(printf '%s' "$model_name" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//; s/opus/opie/; s/fable/fabio/')"
[ -z "$model_disp" ] && model_disp="$model_name"
session_id="$(get '.session_id // ""')"
session_name="$(get '.session_name // empty')"

# (khudson's claude-sessions spool is now module-owned: it is populated by
# UserPromptSubmit/SessionEnd hooks in homies/shimmerjs/home/khudson, not here.)
tpath="$(get '.transcript_path // ""')"
cwd="$(get '.workspace.current_dir // .cwd // "."')"
repo_name="$(get '.workspace.repo.name // empty')"
effort="$(get '.effort.level // empty')"
thinking="$(get '.thinking.enabled // empty')"
fast_mode="$(get '.fast_mode // empty')"
ctx_pct_payload="$(get '.context_window.used_percentage // empty')"
pr_num="$(get '.pr.number // empty')"
pr_state="$(get '.pr.review_state // empty')"
lines_add="$(get '.cost.total_lines_added // empty')"
lines_del="$(get '.cost.total_lines_removed // empty')"

cols="${COLUMNS:-0}"; [ "$cols" -gt 0 ] 2>/dev/null || cols=120
narrow=0; [ "$cols" -lt 80 ] && narrow=1

# ───────────────── location: repo ▸ subpath ⎇ branch (worktree-aware, dedup'd) ─────────────────
top="$(git -C "$cwd" rev-parse --show-toplevel 2>/dev/null)"
in_git=0; [ -n "$top" ] && in_git=1
branch=""; on_trunk=0; detached=0; subpath=""; repo=""
if [ "$in_git" -eq 1 ]; then
  branch="$(git -C "$cwd" rev-parse --abbrev-ref HEAD 2>/dev/null)"
  if [ "$branch" = "HEAD" ] || [ -z "$branch" ]; then
    detached=1
    branch="@$(git -C "$cwd" rev-parse --short HEAD 2>/dev/null)"
  else
    def="$(git -C "$cwd" config worktrunk.default-branch 2>/dev/null)"
    [ -z "$def" ] && def="main"
    [ "$branch" = "$def" ] && on_trunk=1
  fi
  # repo identity: origin-derived name (stable across worktrees) beats the worktree
  # dir basename, which under worktrunk is just the branch again.
  repo="$repo_name"; [ -z "$repo" ] && repo="$(basename "$top")"
  # subpath: where in the tree, relative to the worktree root. Empty at the root.
  case "$cwd" in
    "$top") subpath="" ;;
    "$top"/*) subpath="${cwd#"$top"/}" ;;
    *) subpath="" ;;
  esac
else
  # not a git tree: fall back to a ~-relative cwd as the location
  repo="${cwd/#$HOME/\~}"
fi

now="$(date +%s)"

# ───────────────── live agent/workflow detection (inline, cheap, ~LIVEWIN window) ─────────────────
live_agents=0; live_wf=0
base="${tpath%.jsonl}"
if [ -n "$tpath" ] && [ -d "$base/subagents" ]; then
  ref="${TMPDIR:-/tmp}/.clod-statusline-liveref.$$"
  tb="$(date -v-"${LIVEWIN}"S +%Y%m%d%H%M.%S 2>/dev/null)"
  if [ -n "$tb" ]; then touch -t "$tb" "$ref" 2>/dev/null; else touch -d "-${LIVEWIN} seconds" "$ref" 2>/dev/null; fi
  if [ -f "$ref" ]; then
    live_agents="$(find "$base/subagents" -path '*/workflows/*' -prune -o -name 'agent-*.jsonl' -newer "$ref" -print 2>/dev/null | wc -l | tr -d ' ')"
    live_wf="$(find "$base/subagents/workflows" -type f -newer "$ref" 2>/dev/null \
      | awk -F'/workflows/' '{p=$2; sub(/\/.*/,"",p); if(p!="" && !(p in s)){s[p]=1;n++}} END{print n+0}')"
    rm -f "$ref" 2>/dev/null
  fi
fi
: "${live_agents:=0}" "${live_wf:=0}"

# ───────────────── palette (24-bit; flat foreground, no filled segments) ─────────────────
ESC=$'\033'; R="${ESC}[0m"
DIM="120;126;138"; DIM2="74;82;98"
C_MODEL="196;181;253"; C_EFFORT="240;171;252"; C_THINK="167;139;250"; C_FAST="103;232;249"
C_REPO="45;212;191"; C_BRANCH="125;211;252"; C_TRUNK="110;231;183"
C_OK="74;222;128"; C_WARN="251;191;36"; C_HOT="248;113;113"
C_ADD="74;222;128"; C_DEL="248;113;113"
C_LIVE="34;211;238"
fg(){ printf '%s[38;2;%sm' "$ESC" "$1"; }
seg(){ printf '%s%s%s' "$(fg "$1")" "$2" "$R"; }                 # color, text
lbl(){ printf '%s%s%s' "$(fg "$DIM")" "$1" "$R"; }               # dim label

DOT=" $(fg "$DIM2")·${R} "                                       # separator

# ───────────────── formatters ─────────────────
fmt_tok(){ awk -v n="$1" 'BEGIN{ if(n>=1e9)printf "%.2fB",n/1e9; else if(n>=1e6)printf "%.1fM",n/1e6; else if(n>=1e3)printf "%.1fK",n/1e3; else printf "%d",n }'; }

# smooth context gauge: full blocks + an eighths partial, dim track
PARTS=("▏" "▎" "▍" "▌" "▋" "▊" "▉")
gauge(){ # $1 pct, $2 width, $3 fill-color
  local pct="$1" w="$2" col="$3" eighths full rem out="" empty="" i
  eighths="$(awk -v p="$pct" -v w="$w" 'BEGIN{e=(p/100*w*8)+0.5; if(e<0)e=0; printf "%d",e}')"
  full=$((eighths/8)); rem=$((eighths%8))
  if [ "$full" -ge "$w" ]; then full="$w"; rem=0; fi
  for ((i=0;i<full;i++)); do out="${out}█"; done
  if [ "$rem" -gt 0 ] && [ "$full" -lt "$w" ]; then out="${out}${PARTS[$((rem-1))]}"; full=$((full+1)); fi
  for ((i=full;i<w;i++)); do empty="${empty}░"; done
  printf '%s%s%s%s%s' "$(fg "$col")" "$out" "$(fg "$DIM2")" "$empty" "$R"
}

# ───────────────── context % + color ─────────────────
ctx_pct="$(awk -v p="${ctx_pct_payload:-0}" 'BEGIN{ if(p>100)p=100; if(p<0)p=0; printf "%d",(p+0.5) }')"
if   [ "$ctx_pct" -lt 60 ]; then C_CTX="$C_OK"
elif [ "$ctx_pct" -lt 85 ]; then C_CTX="$C_WARN"
else                             C_CTX="$C_HOT"; fi

# ════════════════════════════ LINE 0 -- session codename (own line when Claude names the chat) ════════════════════════════
# Lower kebab-case for a calmer, codename-y look -- and so a long title gets its
# own line instead of shoving the PLACE line off the right edge.
L0=""
if [ -n "$session_name" ]; then
  session_disp="$(printf '%s' "$session_name" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//')"
  [ -z "$session_disp" ] && session_disp="$session_name"
  L0="$(seg "$DIM" "$session_disp")"
fi

# ════════════════════════════ LINE 1 -- PLACE (identity + location + ctx) ════════════════════════════
L1=""

# model glyph + name, always shown (da fabio/opie); effort only off-xhigh,
# a spark for thinking, a bolt for fast mode.
m="$(seg "$C_MODEL" "◆ ${model_disp}")"
[ -n "$effort" ] && [ "$effort" != "xhigh" ] && m="${m} $(seg "$C_EFFORT" "$effort")"
[ "$thinking" = "true" ] && m="${m} $(seg "$C_THINK" "✳")"
[ "$fast_mode" = "true" ] && m="${m} $(seg "$C_FAST" "⚡")"
L1="${L1}${m}"

# location: repo ▸ subpath  ⎇ branch
L1="${L1}${DOT}$(seg "$C_REPO" "$repo")"
if [ -n "$subpath" ] && [ "$narrow" -eq 0 ]; then
  L1="${L1} $(fg "$DIM2")▸${R} $(seg "$DIM" "$subpath")"
fi
if [ -n "$branch" ] && [ "$branch" != "$repo" ]; then
  if   [ "$detached" -eq 1 ]; then bc="$C_WARN"
  elif [ "$on_trunk" -eq 1 ]; then bc="$C_TRUNK"
  else                             bc="$C_BRANCH"; fi
  L1="${L1}${DOT}$(seg "$bc" "⎇ ${branch}")"
fi

# PR badge (review-state colored), OSC8-linked to the PR when origin is GitHub
if [ -n "$pr_num" ]; then
  case "$pr_state" in
    approved)          pg="✓"; pc="$C_OK" ;;
    changes_requested) pg="✗"; pc="$C_HOT" ;;
    draft)             pg="◌"; pc="$DIM" ;;
    *)                 pg="•"; pc="$C_WARN" ;;
  esac
  pr_label="PR #${pr_num}"
  remote="$(git -C "$cwd" remote get-url origin 2>/dev/null)"
  pr_url=""
  case "$remote" in
    git@github.com:*)        pr_url="https://github.com/${remote#git@github.com:}"; pr_url="${pr_url%.git}/pull/${pr_num}" ;;
    https://github.com/*)    pr_url="${remote%.git}/pull/${pr_num}" ;;
  esac
  if [ -n "$pr_url" ]; then
    pr_label="${ESC}]8;;${pr_url}${ESC}\\${pr_label}${ESC}]8;;${ESC}\\"
  fi
  L1="${L1}${DOT}$(seg "$DIM" "$pr_label") $(seg "$pc" "$pg")"
fi

# context gauge (right-anchored, stable position)
if [ "$narrow" -eq 1 ]; then
  L1="${L1}${DOT}$(lbl "ctx ")$(seg "$C_CTX" "${ctx_pct}%")"
else
  L1="${L1}${DOT}$(lbl "ctx ")$(gauge "$ctx_pct" 8 "$C_CTX") $(seg "$C_CTX" "${ctx_pct}%")"
fi

# ════════════════════════════ LINE 2 -- CHURN (only with real churn) ════════════════════════════
# lines added/removed this session, straight from the payload
L2=""
if { [ -n "$lines_add" ] && [ "$lines_add" -gt 0 ] 2>/dev/null; } || { [ -n "$lines_del" ] && [ "$lines_del" -gt 0 ] 2>/dev/null; }; then
  L2="$(lbl "churn ")$(fg "$C_ADD")+$(fmt_tok "${lines_add:-0}")$(fg "$DIM2")/$(fg "$C_DEL")-$(fmt_tok "${lines_del:-0}")${R}"
fi

# ════════════════════════════ LINE 3 -- FLEET (only while running) ════════════════════════════
L3=""
if [ "$live_agents" -gt 0 ] || [ "$live_wf" -gt 0 ]; then
  # gentle spinner phase from wall-clock seconds (animates across refreshes)
  spin_frames=("◐" "◓" "◑" "◒"); sp="${spin_frames[$(( now % 4 ))]}"
  parts=""
  [ "$live_wf" -gt 0 ]     && parts="${live_wf} workflow$([ "$live_wf" -ne 1 ] && echo s)"
  [ "$live_agents" -gt 0 ] && { sx=""; [ -n "$parts" ] && sx="$(fg "$DIM2") · $(fg "$C_LIVE")"; parts="${parts}${sx}${live_agents} agent$([ "$live_agents" -ne 1 ] && echo s) live"; }
  L3="$(seg "$C_LIVE" "${sp} ")$(fg "$C_LIVE")${parts}${R}"
fi

# ───────────────── emit ─────────────────
[ -n "$L0" ] && printf '%b\n' "$L0"
printf '%b\n' "$L1"
[ -n "$L2" ] && printf '%b\n' "$L2"
[ -n "$L3" ] && printf '%b\n' "$L3"
exit 0
