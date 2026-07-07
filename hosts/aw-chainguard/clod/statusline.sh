#!/usr/bin/env bash
# claude-statusline.sh -- a flat (non-powerline) control-center statusline.
#                         Managed by ~/.universe.
#
# Tuned to how this account actually runs: heavy workflow/subagent fan-out, work
# spread across many repos and worktrunk worktrees, late-night sessions that
# straddle midnight. The model name always shows (the fabio/opie wink), effort
# shows only off-xhigh, location is repo-first, the fleet is first-class, and the
# day window rolls instead of snapping to calendar midnight.
#
# Layout (lines appear/disappear with state -- it grows when you're busy):
#   line 0  session codename   「name」          -- ONLY when Claude has named the chat
#   line 1  PLACE              ◆ model  repo ▸ subpath  ⎇ branch  PR  ctx gauge
#   line 2  LEDGER             session tokens (+$) · churn · burn + heat tape · 24h
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
# SPEND is the headline, measured in raw tokens (input+output+cache) -- exact, and the
# dollar adjunct uses Claude Code's own cost.total_cost_usd (no hardcoded rate table).
# Session spend scans this session's transcripts INCLUDING subagents/ and workflows/,
# so heavy fan-out is counted. Only session + a rolling 24h are shown (no all-time
# scan) -- a 2-day recent window.
#
# Caching (so the line always renders instantly, scans happen in the background):
#   ~/.claude/.statusline_cache/<session>.json  session/24h + fleet tallies + spend tape
# Live agent/workflow detection is inline (cheap fs scan, ~LIVEWIN-second window).
#
# Tunables (env):
#   STATUSLINE_TTL       seconds between recent-pass refreshes           (default 60)
#   STATUSLINE_LIVEWIN   seconds an agent counts as "live" after last write (default 25)
#   STATUSLINE_NOUSD     if set, hide the dollar adjunct on the session odometer
#   STATUSLINE_SYNC      if set, compute synchronously (used for testing)

export PATH="/etc/profiles/per-user/$USER/bin:/run/current-system/sw/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:$PATH"

JQ="$(command -v jq || true)"
PROJ="$HOME/.claude/projects"
CACHE_DIR="$HOME/.claude/.statusline_cache"
TTL="${STATUSLINE_TTL:-60}"
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
ctx_size="$(get '.context_window.context_window_size // empty')"
pr_num="$(get '.pr.number // empty')"
pr_state="$(get '.pr.review_state // empty')"
usd="$(get '.cost.total_cost_usd // empty')"
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

# ───────────────── rolling 24h boundary ─────────────────
now="$(date +%s)"
day_start=$((now - 86400))

# ───────────────── recent pass: session / 24h (+ ctx fallback, fleet tallies, spend tape) ─────────────────
# ses_tok is comprehensive: every assistant turn whose transcript path carries the
# session id, which includes this session's subagents/ and workflows/ agent files.
compute_recent() {
  local now="$1"
  find "$PROJ" -name '*.jsonl' -mtime -2 -print0 2>/dev/null \
  | xargs -0 "$JQ" -r '
      select(.type=="assistant" and (.message.usage != null)) |
      [ (.timestamp | sub("\\.[0-9]+Z$";"Z") | fromdateiso8601),
        ((.message.id // .uuid) + ":" + (.requestId // "")),
        (.message.model // "opus"),
        (.message.usage.input_tokens // 0),
        (.message.usage.output_tokens // 0),
        (.message.usage.cache_read_input_tokens // 0),
        (.message.usage.cache_creation.ephemeral_5m_input_tokens // .message.usage.cache_creation_input_tokens // 0),
        (.message.usage.cache_creation.ephemeral_1h_input_tokens // 0),
        input_filename
      ] | @tsv' 2>/dev/null \
  | awk -F'\t' -v sid="$session_id" -v tp="$tpath" -v d0="$day_start" -v now="$now" '
      { key=$2; if (seen[key]++) next
        ep=$1+0; i=$4+0; o=$5+0; cr=$6+0; c5=$7+0; c1=$8+0; fn=$9
        tok = i+o+cr+c5+c1
        isses = (sid != "" && index(fn,sid) > 0) || (sid == "" && fn == tp)
        if (isses) {
          st+=tok
          np++; sep[np]=ep; sco[np]=tok                  # token tape points (bucketed in END)
          if (sfirst==0 || ep<sfirst) sfirst=ep
          if (fn ~ /\/workflows\/wf_/)                        { wfaset[fn]=1; s=fn; sub(/.*\/workflows\//,"",s); sub(/\/.*/,"",s); wfset[s]=1 }
          else if (fn ~ /\/subagents\// && fn ~ /\/agent-/)   { subset[fn]=1 }
        }
        if (ep >= d0) { dt+=tok }
        if (fn == tp && ep > lastep) { lastep=ep; ctxp = i+cr+c5+c1 }
      }
      END {
        ns=0;  for (k in subset) ns++
        nwa=0; for (k in wfaset) nwa++
        nw=0;  for (k in wfset)  nw++
        NB=13
        span = (sfirst>0 ? now - sfirst : 0); if (span<1) span=1
        bw = span/NB
        for (b=0;b<NB;b++) bk[b]=0
        for (j=1;j<=np;j++){ b=int((sep[j]-sfirst)/bw); if(b<0)b=0; if(b>=NB)b=NB-1; bk[b]+=sco[j] }
        series="["
        for (b=0;b<NB;b++) series=series (b>0?",":"") sprintf("%d", bk[b]+0)
        series=series "]"
        printf "{\"ts\":%d,\"ses_tok\":%d,\"day_tok\":%d,\"ctx_tok\":%d,\"n_sub\":%d,\"n_wfa\":%d,\"n_wf\":%d,\"ses_first\":%d,\"series\":%s}", \
               now, st, dt, ctxp, ns, nwa, nw, sfirst, series
      }'
}

# ───────────────── refresh cache (background unless STATUSLINE_SYNC) ─────────────────
mkdir -p "$CACHE_DIR"
CF="$CACHE_DIR/${session_id:-default}.json"

refresh() { # $1=cachefile  $2=ttl  $3=compute-fn
  local cf="$1" ttl="$2" fn="$3" lock="$1.lock" cts fresh=0
  if [ -f "$cf" ]; then
    cts="$("$JQ" -r '.ts // 0' "$cf" 2>/dev/null)"; [ -z "$cts" ] && cts=0
    [ $((now - cts)) -lt "$ttl" ] && fresh=1
  fi
  if [ -n "$STATUSLINE_SYNC" ]; then
    "$fn" "$now" > "$cf.tmp.$$" 2>/dev/null && mv "$cf.tmp.$$" "$cf"
    return
  fi
  # stale lock reaper (>2min)
  if [ -d "$lock" ] && [ -n "$(find "$lock" -prune -mmin +2 2>/dev/null)" ]; then rmdir "$lock" 2>/dev/null; fi
  if [ "$fresh" -eq 0 ] && mkdir "$lock" 2>/dev/null; then
    ( "$fn" "$now" > "$cf.tmp.$$" 2>/dev/null && mv "$cf.tmp.$$" "$cf"; rmdir "$lock" 2>/dev/null ) </dev/null >/dev/null 2>&1 &
  fi
}
refresh "$CF" "$TTL" compute_recent

# ───────────────── read cache (zeros until first refresh lands) ─────────────────
ses_tok=0; day_tok=0; ctx_tok=0; n_sub=0; n_wfa=0; n_wf=0; ses_first=0
series_raw=""
if [ -f "$CF" ]; then
  read -r ses_tok day_tok ctx_tok n_sub n_wfa n_wf ses_first \
    < <("$JQ" -r '[.ses_tok,.day_tok,.ctx_tok,.n_sub,.n_wfa,.n_wf,.ses_first]|@tsv' "$CF" 2>/dev/null)
  series_raw="$("$JQ" -r '.series // [] | @tsv' "$CF" 2>/dev/null)"
fi
: "${ses_tok:=0}" "${day_tok:=0}" "${ctx_tok:=0}"
: "${n_sub:=0}" "${n_wfa:=0}" "${n_wf:=0}" "${ses_first:=0}"
read -ra spend_series <<<"$series_raw"

# ───────────────── live agent/workflow detection (inline, cheap, ~LIVEWIN window) ─────────────────
live_agents=0; live_wf=0
base="${tpath%.jsonl}"
if [ -n "$tpath" ] && [ -d "$base/subagents" ]; then
  ref="$CACHE_DIR/.liveref.$$"
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
C_SES="134;239;172"; C_DAY="250;204;21"; C_USD="148;163;184"
C_LIVE="34;211;238"
fg(){ printf '%s[38;2;%sm' "$ESC" "$1"; }
seg(){ printf '%s%s%s' "$(fg "$1")" "$2" "$R"; }                 # color, text
lbl(){ printf '%s%s%s' "$(fg "$DIM")" "$1" "$R"; }               # dim label

DOT=" $(fg "$DIM2")·${R} "                                       # separator

# ───────────────── formatters ─────────────────
fmt_tok(){ awk -v n="$1" 'BEGIN{ if(n>=1e9)printf "%.2fB",n/1e9; else if(n>=1e6)printf "%.1fM",n/1e6; else if(n>=1e3)printf "%.1fK",n/1e3; else printf "%d",n }'; }
fmt_usd(){ awk -v n="$1" 'BEGIN{ if(n>=100)printf "$%d",n+0.5; else if(n>=0.01)printf "$%.2f",n; else if(n>0)printf "<$0.01"; else printf "$0" }'; }

# spend tape: heat-colored sparkline, cool (green) -> hot (red) by relative magnitude
heatfg(){ awk -v t="$1" 'BEGIN{ if(t<0)t=0; if(t>1)t=1;
  if(t<0.5){u=t/0.5; r=74+(250-74)*u; g=222+(204-222)*u; b=128+(21-128)*u}
  else{u=(t-0.5)/0.5; r=250+(248-250)*u; g=204+(113-204)*u; b=21+(113-21)*u}
  printf "%d;%d;%d", r,g,b }'; }
SPARKS=("▁" "▂" "▃" "▄" "▅" "▆" "▇" "█")
sparkline(){ # args: the series values; renders one heat-colored bar each
  local max=0 v t idx out=""
  for v in "$@"; do awk -v a="$v" -v m="$max" 'BEGIN{exit !(a>m)}' && max="$v"; done
  for v in "$@"; do
    t="$(awk -v v="$v" -v m="$max" 'BEGIN{print (m>0? v/m:0)}')"
    idx="$(awk -v t="$t" 'BEGIN{i=int(t*7+0.5); if(i<0)i=0; if(i>7)i=7; print i}')"
    out="${out}$(fg "$(heatfg "$t")")${SPARKS[$idx]}"
  done
  printf '%s%s' "$out" "$R"
}

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
if [ -n "$ctx_pct_payload" ]; then
  ctx_pct="$(awk -v p="$ctx_pct_payload" 'BEGIN{ if(p>100)p=100; if(p<0)p=0; printf "%d",(p+0.5) }')"
else
  win="${ctx_size:-200000}"; [ "$win" -gt 0 ] 2>/dev/null || win=200000
  ctx_pct="$(awk -v t="$ctx_tok" -v w="$win" 'BEGIN{ p=(w>0? t/w*100:0); if(p>100)p=100; printf "%d",(p+0.5) }')"
fi
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

# ════════════════════════════ LINE 2 -- LEDGER (spend tape) ════════════════════════════
# session odometer: token count (green), a dim dollar adjunct (Claude Code's own
# estimate), and a braille tick that rolls every refresh.
spin_b=("⠋" "⠙" "⠹" "⠸" "⠼" "⠴" "⠦" "⠧" "⠇" "⠏")
tick=$((now/5))
odo="$(fg "$C_SES")$(fmt_tok "$ses_tok")$(fg "$DIM2") tok"
if [ -z "$STATUSLINE_NOUSD" ] && [ -n "$usd" ] && awk -v u="$usd" 'BEGIN{exit !(u>0)}'; then
  odo="${odo} $(fg "$C_USD")~$(fmt_usd "$usd")"
fi
odo="${odo} $(fg "$C_SES")${spin_b[$((tick%10))]}${R}"
L2="$(lbl "spend ")${odo} $(lbl "session")"

# churn: lines added/removed this session (produced, next to consumed)
if { [ -n "$lines_add" ] && [ "$lines_add" -gt 0 ] 2>/dev/null; } || { [ -n "$lines_del" ] && [ "$lines_del" -gt 0 ] 2>/dev/null; }; then
  L2="${L2}${DOT}$(fg "$C_ADD")+$(fmt_tok "${lines_add:-0}")$(fg "$DIM2")/$(fg "$C_DEL")-$(fmt_tok "${lines_del:-0}")${R}"
fi

# is there any real spend in the tape window? (gates burn + sparkline)
series_sum="$(awk -v s="$series_raw" 'BEGIN{n=split(s,a,/[ \t]+/); for(i=1;i<=n;i++)t+=a[i]; print (t>0?1:0)}')"

# burn rate tokens/hr over the session so far -- only meaningful with a real session start
if [ "$ses_first" -gt 0 ] 2>/dev/null; then
  burn="$(awk -v t="$ses_tok" -v f="$ses_first" -v now="$now" 'BEGIN{ d=now-f; if(d<120)d=120; printf "%d", t/(d/3600) }')"
  if [ "$burn" -gt 0 ] 2>/dev/null; then
    # acceleration: late third of the tape vs the prior third. Ticker convention:
    # up is green, down is red (inverted from heat -- the line going up reads
    # positive here, not as a warning).
    accel="$(awk -v s="$series_raw" 'BEGIN{ n=split(s,a,/[ \t]+/); if(n<6){print "flat"; exit}
      k=int(n/3); late=0; early=0; for(i=n-k+1;i<=n;i++)late+=a[i]; for(i=n-2*k+1;i<=n-k;i++)early+=a[i];
      if(late>early*1.15)print "up"; else if(late<early*0.85)print "down"; else print "flat" }')"
    case "$accel" in
      up)   car="▲"; cc="$C_OK" ;;
      down) car="▼"; cc="$C_HOT" ;;
      *)    car="▬"; cc="$DIM" ;;
    esac
    L2="${L2}   $(fg "$cc")${car} $(fmt_tok "$burn")/hr${R}"
  fi
fi

# the heat tape itself (wide terminals, and only when the window holds real spend)
if [ "$narrow" -eq 0 ] && [ "$cols" -ge 90 ] && [ "$series_sum" = "1" ]; then
  L2="${L2} $(sparkline "${spend_series[@]}")"
fi

# rolling 24h
L2="${L2}${DOT}$(seg "$C_DAY" "$(fmt_tok "$day_tok")") $(lbl "24h")"

# quiet fleet tally (agents + workflows spawned this session) when nothing is live now.
# n_wfa counts the agents inside workflows -- the bulk of the fan-out.
if { [ "$n_sub" -gt 0 ] || [ "$n_wfa" -gt 0 ] || [ "$n_wf" -gt 0 ]; } && [ "$live_agents" -eq 0 ] && [ "$live_wf" -eq 0 ]; then
  agents=$((n_sub + n_wfa))
  t=""
  [ "$agents" -gt 0 ] && t="${agents} agent$([ "$agents" -ne 1 ] && echo s)"
  [ "$n_wf" -gt 0 ]   && { sx=""; [ -n "$t" ] && sx=" · "; t="${t}${sx}${n_wf} wf"; }
  [ -n "$t" ] && L2="${L2}${DOT}$(seg "$DIM" "⚙ ${t}")"
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
printf '%b\n' "$L2"
[ -n "$L3" ] && printf '%b\n' "$L3"
exit 0
