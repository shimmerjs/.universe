#!/usr/bin/env bash
# claude-statusline.sh — a fresh, flat (non-powerline) multiline statusline.
#                        Managed by ~/.universe.
#
# Layout (lines appear/disappear with state — it grows when you're busy):
#   line 1  identity + context   ◆ model effort  ⎇ branch  ⌥ worktree  ctx gauge  PR
#   line 2  the spend tape        session odometer · burn rate · heat sparkline · today
#   line 3  live activity         ⟳ N agents · M workflows  — ONLY while they're running
#
# Spend is the headline. Session spend is the real, comprehensive number: it scans
# this session's transcripts INCLUDING subagents/ and workflows/ (so heavy fan-out is
# counted), priced per-model from token usage. The payload's .cost.total_cost_usd is
# used only as an instant fallback before the first scan lands, so the line is never
# blank or wrong-low at session start. Only session + today are shown (no all-time
# total), so there is no periodic full-history scan — just an 8-day recent window.
#
# Costs are estimates priced from transcript token usage (Claude doesn't ship costUSD).
#
# Caching (so the line always renders instantly, scans happen in the background):
#   ~/.claude/.statusline_cache/<session>.json   session/today/week + spend tape — TTL STATUSLINE_TTL
# Live agent/workflow detection is done inline (cheap fs scan, ~LIVEWIN-second window).
#
# Tunables (env):
#   STATUSLINE_TTL       seconds between session/today/week refreshes  (default 30)
#   STATUSLINE_LIVEWIN   seconds an agent counts as "live" after last write (default 25)
#   STATUSLINE_SYNC      if set, compute synchronously (used for testing)

export PATH="/etc/profiles/per-user/$USER/bin:/run/current-system/sw/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:$PATH"

JQ="$(command -v jq || true)"
PROJ="$HOME/.claude/projects"
CACHE_DIR="$HOME/.claude/.statusline_cache"
TTL="${STATUSLINE_TTL:-30}"
LIVEWIN="${STATUSLINE_LIVEWIN:-25}"

input="$(cat)"

if [ -z "$JQ" ]; then
  printf 'claude — install jq for the rich statusline\n'
  exit 0
fi

# ───────────────────────── payload ─────────────────────────
get(){ printf '%s' "$input" | "$JQ" -r "$1" 2>/dev/null; }
model_name="$(get '.model.display_name // .model.id // "Claude"')"
# lower kebab-case the model display name, with a wink: opus -> opie
model_disp="$(printf '%s' "$model_name" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//; s/opus/opie/')"
[ -z "$model_disp" ] && model_disp="$model_name"
session_id="$(get '.session_id // ""')"
session_name="$(get '.session_name // empty')"
tpath="$(get '.transcript_path // ""')"
cwd="$(get '.workspace.current_dir // .cwd // "."')"
effort="$(get '.effort.level // empty')"
thinking="$(get '.thinking.enabled // empty')"
worktree="$(get '.worktree.name // .workspace.git_worktree // empty')"
cost_payload="$(get '.cost.total_cost_usd // empty')"
ctx_pct_payload="$(get '.context_window.used_percentage // empty')"
ctx_size="$(get '.context_window.context_window_size // empty')"
over200k="$(get '.exceeds_200k_tokens // empty')"
pr_num="$(get '.pr.number // empty')"
pr_state="$(get '.pr.review_state // empty')"
q5="$(get '.rate_limits.five_hour.used_percentage // empty')"
q7="$(get '.rate_limits.seven_day.used_percentage // empty')"

branch="$(git -C "$cwd" rev-parse --abbrev-ref HEAD 2>/dev/null)"
[ -z "$branch" ] && branch="$(basename "$cwd")"

cols="${COLUMNS:-0}"; [ "$cols" -gt 0 ] 2>/dev/null || cols=120
narrow=0; [ "$cols" -lt 80 ] && narrow=1

# ───────────────── local day/week boundaries (GNU date primary, BSD fallback) ─────────────────
epoch_at(){ date -d "$1" +%s 2>/dev/null || date -j -f '%Y-%m-%d %H:%M:%S' "$1" +%s 2>/dev/null; }
today="$(date +%Y-%m-%d)"
dow="$(date +%u)"                                   # 1=Mon .. 7=Sun
wsd="$(date -d "$today -$((dow-1)) days" +%Y-%m-%d 2>/dev/null || date -j -v-$((dow-1))d +%Y-%m-%d 2>/dev/null)"
today_start="$(epoch_at "$today 00:00:00")"
week_start="$(epoch_at "$wsd 00:00:00")"
: "${today_start:=0}" "${week_start:=0}"

# ───────────────── recent pass: session / today / week (+ ctx fallback, tallies, spend tape) ─────────────────
compute_recent() {
  local now="$1"
  find "$PROJ" -name '*.jsonl' -mtime -8 -print0 2>/dev/null \
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
  | awk -F'\t' -v sid="$session_id" -v tp="$tpath" -v t0="$today_start" -v w0="$week_start" -v now="$now" '
      function rate(m,k,   r){
        if      (m ~ /opus/)   { r["in"]=15;  r["out"]=75; r["cr"]=1.5;  r["c5"]=18.75; r["c1"]=30 }
        else if (m ~ /sonnet/) { r["in"]=3;   r["out"]=15; r["cr"]=0.30; r["c5"]=3.75;  r["c1"]=6  }
        else if (m ~ /haiku/)  { r["in"]=1;   r["out"]=5;  r["cr"]=0.10; r["c5"]=1.25;  r["c1"]=2  }
        else                   { r["in"]=15;  r["out"]=75; r["cr"]=1.5;  r["c5"]=18.75; r["c1"]=30 }
        return r[k]
      }
      { key=$2; if (seen[key]++) next
        ep=$1+0; m=$3; i=$4+0; o=$5+0; cr=$6+0; c5=$7+0; c1=$8+0; fn=$9
        tok = i+o+cr+c5+c1
        cost = (i*rate(m,"in") + o*rate(m,"out") + cr*rate(m,"cr") + c5*rate(m,"c5") + c1*rate(m,"c1")) / 1e6
        isses = (sid != "" && index(fn,sid) > 0) || (sid == "" && fn == tp)
        if (isses) {
          st+=tok; sc+=cost
          np++; sep[np]=ep; sco[np]=cost                 # spend tape points (bucketed in END)
          if (sfirst==0 || ep<sfirst) sfirst=ep
          if (fn ~ /\/workflows\/wf_/)                        { s=fn; sub(/.*\/workflows\//,"",s); sub(/\/.*/,"",s); wfset[s]=1 }
          else if (fn ~ /\/subagents\// && fn ~ /\/agent-/)   { subset[fn]=1 }
        }
        if (ep >= t0) { dt+=tok; dc+=cost }
        if (ep >= w0) { wt+=tok; wc+=cost }
        if (fn == tp && ep > lastep) { lastep=ep; ctxp = i+cr+c5+c1 }
      }
      END {
        ns=0; for (k in subset) ns++
        nw=0; for (k in wfset)  nw++
        NB=13
        span = (sfirst>0 ? now - sfirst : 0); if (span<1) span=1
        bw = span/NB
        for (b=0;b<NB;b++) bk[b]=0
        for (j=1;j<=np;j++){ b=int((sep[j]-sfirst)/bw); if(b<0)b=0; if(b>=NB)b=NB-1; bk[b]+=sco[j] }
        series="["
        for (b=0;b<NB;b++) series=series (b>0?",":"") sprintf("%.4f", bk[b]+0)
        series=series "]"
        printf "{\"ts\":%d,\"ses_tok\":%d,\"ses_cost\":%.4f,\"day_tok\":%d,\"day_cost\":%.4f,\"wk_tok\":%d,\"wk_cost\":%.4f,\"ctx_tok\":%d,\"n_sub\":%d,\"n_wf\":%d,\"ses_first\":%d,\"series\":%s}", \
               now, st,sc, dt,dc, wt,wc, ctxp, ns, nw, sfirst, series
      }'
}

# ───────────────── refresh cache (background unless STATUSLINE_SYNC) ─────────────────
mkdir -p "$CACHE_DIR"
now="$(date +%s)"
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
ses_tok=0; ses_cost=0; day_tok=0; day_cost=0; wk_tok=0; wk_cost=0; ctx_tok=0; n_sub=0; n_wf=0; ses_first=0
series_raw=""
if [ -f "$CF" ]; then
  read -r ses_tok ses_cost day_tok day_cost wk_tok wk_cost ctx_tok n_sub n_wf ses_first \
    < <("$JQ" -r '[.ses_tok,.ses_cost,.day_tok,.day_cost,.wk_tok,.wk_cost,.ctx_tok,.n_sub,.n_wf,.ses_first]|@tsv' "$CF" 2>/dev/null)
  series_raw="$("$JQ" -r '.series // [] | @tsv' "$CF" 2>/dev/null)"
fi
: "${ses_tok:=0}" "${ses_cost:=0}" "${day_tok:=0}" "${day_cost:=0}" "${wk_tok:=0}" "${wk_cost:=0}"
: "${ctx_tok:=0}" "${n_sub:=0}" "${n_wf:=0}" "${ses_first:=0}"
read -ra spend_series <<<"$series_raw"

# session cost: prefer comprehensive scan, fall back to payload estimate so it's never wrong-low
ses_cost="$(awk -v a="$ses_cost" -v b="${cost_payload:-0}" 'BEGIN{print (a>b?a:b)}')"

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
C_MODEL="196;181;253"; C_EFFORT="240;171;252"; C_THINK="167;139;250"
C_BRANCH="125;211;252"; C_WTREE="251;191;36"
C_OK="74;222;128"; C_WARN="251;191;36"; C_HOT="248;113;113"
C_SES="134;239;172"; C_DAY="250;204;21"
C_LIVE="34;211;238"
fg(){ printf '%s[38;2;%sm' "$ESC" "$1"; }
seg(){ printf '%s%s%s' "$(fg "$1")" "$2" "$R"; }                 # color, text
lbl(){ printf '%s%s%s' "$(fg "$DIM")" "$1" "$R"; }               # dim label

DOT=" $(fg "$DIM2")·${R} "                                        # separator

# ───────────────── formatters ─────────────────
fmt_tok(){ awk -v n="$1" 'BEGIN{ if(n>=1e9)printf "%.2fB",n/1e9; else if(n>=1e6)printf "%.1fM",n/1e6; else if(n>=1e3)printf "%.1fK",n/1e3; else printf "%d",n }'; }
fmt_money(){ awk -v n="$1" 'BEGIN{ if(n>=10000)printf "$%.1fk",n/1000; else if(n>=1000)printf "$%.0f",n; else printf "$%.2f",n }'; }

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

# ════════════════════════════ LINE 1 — identity + context ════════════════════════════
L1=""
[ -n "$session_name" ] && L1="${L1}$(lbl "「")$(seg "$DIM" "$session_name")$(lbl "」") "

# model ◆ + effort + thinking
m="$(seg "$C_MODEL" "◆ ${model_disp}")"
[ -n "$effort" ] && [ "$effort" != "high" ] && m="${m} $(seg "$C_EFFORT" "$effort")"
[ "$thinking" = "true" ] && m="${m} $(seg "$C_THINK" "✳")"
L1="${L1}${m}"

# branch
L1="${L1}${DOT}$(seg "$C_BRANCH" "⎇ ${branch}")"

# worktree badge
[ -n "$worktree" ] && L1="${L1}${DOT}$(seg "$C_WTREE" "⌥ ${worktree}")"

# context gauge
if [ "$narrow" -eq 1 ]; then
  L1="${L1}${DOT}$(lbl "ctx ")$(seg "$C_CTX" "${ctx_pct}%")"
else
  L1="${L1}${DOT}$(lbl "ctx ")$(gauge "$ctx_pct" 8 "$C_CTX") $(seg "$C_CTX" "${ctx_pct}%")"
fi
[ "$over200k" = "true" ] && L1="${L1} $(seg "$C_HOT" "200k+")"

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

# rate-limit quota (only when payload carries it — Pro/Max)
if [ -n "$q5" ] || [ -n "$q7" ]; then
  qmax="$(awk -v a="${q5:-0}" -v b="${q7:-0}" 'BEGIN{print (a>b?a:b)}')"
  if   awk -v p="$qmax" 'BEGIN{exit !(p<50)}'; then qc="$C_OK"
  elif awk -v p="$qmax" 'BEGIN{exit !(p<80)}'; then qc="$C_WARN"
  else                                              qc="$C_HOT"; fi
  qp=""
  [ -n "$q5" ] && qp="5h $(awk -v v="$q5" 'BEGIN{printf "%d",v+0.5}')%"
  [ -n "$q7" ] && { sx=""; [ -n "$qp" ] && sx=" · "; qp="${qp}${sx}7d $(awk -v v="$q7" 'BEGIN{printf "%d",v+0.5}')%"; }
  L1="${L1}${DOT}$(seg "$qc" "⏱ ${qp}")"
fi

# ════════════════════════════ LINE 2 — the spend tape ════════════════════════════
# session odometer (dollars green, cents dim, a braille tick that rolls every refresh)
spin_b=("⠋" "⠙" "⠹" "⠸" "⠼" "⠴" "⠦" "⠧" "⠇" "⠏")
tick=$((now/5))
read -r ses_int ses_cts < <(awk -v c="$ses_cost" 'BEGIN{ tc=int(c*100+0.5); printf "%d %02d", int(tc/100), tc%100 }')
odo="$(fg "$C_SES")\$${ses_int}$(fg "$DIM2").${ses_cts}$(fg "$C_SES")${spin_b[$((tick%10))]}${R}"
L2="$(lbl "spend ")${odo} $(lbl "session")"

# is there any real spend in the tape window? (gates burn + sparkline)
series_sum="$(awk -v s="$series_raw" 'BEGIN{n=split(s,a,/[ \t]+/); for(i=1;i<=n;i++)t+=a[i]; print (t>0?1:0)}')"

# burn rate $/hr over the session so far — only meaningful with a real session start
if [ "$ses_first" -gt 0 ] 2>/dev/null; then
  burn="$(awk -v c="$ses_cost" -v f="$ses_first" -v now="$now" 'BEGIN{ d=now-f; if(d<120)d=120; printf "%.2f", c/(d/3600) }')"
  if awk -v b="$burn" 'BEGIN{exit !(b>0)}'; then
    # acceleration: late third of the tape vs the prior third -> rising spend is the hot one
    accel="$(awk -v s="$series_raw" 'BEGIN{ n=split(s,a,/[ \t]+/); if(n<6){print "flat"; exit}
      k=int(n/3); late=0; early=0; for(i=n-k+1;i<=n;i++)late+=a[i]; for(i=n-2*k+1;i<=n-k;i++)early+=a[i];
      if(late>early*1.15)print "up"; else if(late<early*0.85)print "down"; else print "flat" }')"
    case "$accel" in
      up)   car="▲"; cc="$C_HOT" ;;
      down) car="▼"; cc="$C_OK" ;;
      *)    car="▬"; cc="$DIM" ;;
    esac
    L2="${L2}   $(fg "$cc")${car} \$${burn}/hr${R}"
  fi
fi

# the heat tape itself (wide terminals, and only when the window holds real spend)
if [ "$narrow" -eq 0 ] && [ "$cols" -ge 90 ] && [ "$series_sum" = "1" ]; then
  L2="${L2}   $(sparkline "${spend_series[@]}")"
fi

# today
L2="${L2}${DOT}$(seg "$C_DAY" "$(fmt_money "$day_cost")") $(lbl "today")"

# quiet tally of agents/workflows spawned this session (when not currently live)
if { [ "$n_sub" -gt 0 ] || [ "$n_wf" -gt 0 ]; } && [ "$live_agents" -eq 0 ] && [ "$live_wf" -eq 0 ]; then
  t=""
  [ "$n_sub" -gt 0 ] && t="${n_sub} agent$([ "$n_sub" -ne 1 ] && echo s)"
  [ "$n_wf" -gt 0 ]  && { sx=""; [ -n "$t" ] && sx=" · "; t="${t}${sx}${n_wf} wf"; }
  L2="${L2}${DOT}$(seg "$DIM" "⚙ ${t}")"
fi

# ════════════════════════════ LINE 3 — live activity (only while running) ════════════════════════════
L3=""
if [ "$live_agents" -gt 0 ] || [ "$live_wf" -gt 0 ]; then
  # gentle spinner phase from wall-clock seconds (animates across refreshes)
  spin_frames=("◐" "◓" "◑" "◒"); sp="${spin_frames[$(( now % 4 ))]}"
  parts=""
  [ "$live_agents" -gt 0 ] && parts="${live_agents} agent$([ "$live_agents" -ne 1 ] && echo s) live"
  [ "$live_wf" -gt 0 ]     && { sx=""; [ -n "$parts" ] && sx="$(fg "$DIM2") · $(fg "$C_LIVE")"; parts="${parts}${sx}${live_wf} workflow$([ "$live_wf" -ne 1 ] && echo s)"; }
  L3="$(seg "$C_LIVE" "${sp} ")$(fg "$C_LIVE")${parts}${R}"
fi

# ───────────────── emit ─────────────────
printf '%b\n' "$L1"
printf '%b\n' "$L2"
[ -n "$L3" ] && printf '%b\n' "$L3"
exit 0
