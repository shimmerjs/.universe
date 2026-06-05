#!/usr/bin/env bash
# claude-subagent-statusline.sh — custom rows for the subagent panel. Managed by ~/.universe.
#
# Claude Code calls this once per refresh tick with a JSON payload carrying a `tasks`
# array (one entry per visible subagent/workflow row) and a top-level `columns` width.
# For each task we emit one JSON line: {"id": "<task id>", "content": "<row>"}.
# Emitting an EMPTY content string hides that row — we use that to drop agents that
# have been queued but haven't actually started yet, so the panel shows live work.
#
# Row:  <status>  label [type]  ·  <tokens> ⟨velocity tape⟩  ·  <elapsed>
#
#   status glyph+color: running ⟳ cyan · done ✓ green · failed ✗ red · queued ◌ dim.
#   [type]         the agent type (researcher/skeptic/...), dim, when it adds info.
#   velocity tape  a braille sparkline of .tokenSamples — how fast this agent is cooking.
#                  Drawn only when the panel is wide enough (.columns) to afford it.
#
# This is the realtime, per-agent companion to the main statusline's compact "live" row.

export PATH="/etc/profiles/per-user/$USER/bin:/run/current-system/sw/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:$PATH"
JQ="$(command -v jq || true)"
[ -z "$JQ" ] && exit 0

input="$(cat)"
now="$(date +%s)"
columns="$("$JQ" -r '.columns // 0' <<<"$input" 2>/dev/null)"; : "${columns:=0}"

ESC=$'\033'; R="${ESC}[0m"
DIM="120;126;138"; DIM2="74;82;98"
C_RUN="34;211;238"; C_OK="74;222;128"; C_BAD="248;113;113"; C_QUEUE="148;163;184"; C_LABEL="226;232;240"
fg(){ printf '%s[38;2;%sm' "$ESC" "$1"; }

ftok(){ awk -v n="$1" 'BEGIN{ if(n>=1e6)printf "%.1fM",n/1e6; else if(n>=1e3)printf "%.1fK",n/1e3; else printf "%d",n }'; }
fdur(){ awk -v s="$1" 'BEGIN{ s=int(s); if(s<0)s=0; if(s>=3600)printf "%d:%02d:%02d",s/3600,(s%3600)/60,s%60; else if(s>=60)printf "%d:%02d",s/60,s%60; else printf "%ds",s }'; }

# braille velocity tape from token samples (no color; caller wraps it)
SP=("▁" "▂" "▃" "▄" "▅" "▆" "▇" "█")
tape(){ # args: numeric samples
  local max=0 v idx out=""
  for v in "$@"; do awk -v a="$v" -v m="$max" 'BEGIN{exit !(a>m)}' && max="$v"; done
  for v in "$@"; do
    idx="$(awk -v v="$v" -v m="$max" 'BEGIN{i=(m>0? int(v/m*7+0.5):0); if(i<0)i=0; if(i>7)i=7; print i}')"
    out="${out}${SP[$idx]}"
  done
  printf '%s' "$out"
}

# tab-separated: id, status, type, label, tokenCount, startTime, samples(comma-joined)
"$JQ" -r '.tasks[]? | [
    (.id // ""), (.status // ""), (.type // ""),
    (.label // .name // .description // "agent"),
    (.tokenCount // 0), (.startTime // ""),
    ((.tokenSamples // []) | map(tostring) | join(","))
  ] | @tsv' <<<"$input" 2>/dev/null \
| while IFS=$'\t' read -r id status type label tokens start samples; do
    [ -z "$id" ] && continue

    # empty-content row-hiding: a queued agent that hasn't started (no tokens, no
    # start) is just noise in a big fan-out -> hide it until it actually begins.
    case "$status" in
      queued|pending|waiting)
        if [ "${tokens:-0}" -eq 0 ] 2>/dev/null && { [ -z "$start" ] || [ "$start" = "null" ]; }; then
          "$JQ" -nc --arg id "$id" '{id:$id, content:""}'
          continue
        fi ;;
    esac

    case "$status" in
      running|in_progress|active) g="⟳"; c="$C_RUN" ;;
      completed|done|success)     g="✓"; c="$C_OK" ;;
      failed|error|cancelled)     g="✗"; c="$C_BAD" ;;
      queued|pending|waiting)     g="◌"; c="$C_QUEUE" ;;
      *)                          g="•"; c="$DIM" ;;
    esac

    row="$(fg "$c")${g}${R} $(fg "$C_LABEL")${label}${R}"

    # agent type tag, when it isn't just echoing the label
    if [ -n "$type" ] && [ "$type" != "null" ]; then
      lc_label="$(printf '%s' "$label" | tr '[:upper:]' '[:lower:]')"
      lc_type="$(printf '%s' "$type" | tr '[:upper:]' '[:lower:]')"
      if [ "$lc_type" != "$lc_label" ]; then
        row="${row} $(fg "$DIM2")[${lc_type}]${R}"
      fi
    fi

    if [ "${tokens:-0}" -gt 0 ] 2>/dev/null; then
      row="${row} $(fg "$DIM2")·${R} $(fg "$DIM")$(ftok "$tokens") tok${R}"
      # velocity tape from samples — only when the panel is wide enough to afford it
      if [ -n "$samples" ] && { [ "$columns" -eq 0 ] || [ "$columns" -ge 60 ]; }; then
        IFS=',' read -ra svals <<<"$samples"
        if [ "${#svals[@]}" -ge 2 ]; then
          row="${row} $(fg "$c")$(tape "${svals[@]}")${R}"
        fi
      fi
    fi

    # elapsed: normalize startTime (ms or s epoch) → seconds, then now-start
    secs=""
    case "$start" in
      ''|null) ;;
      *[!0-9]*) ;;                                   # non-numeric → skip
      *) if [ "$start" -gt 100000000000 ] 2>/dev/null; then secs=$(( now - start/1000 ))
         else secs=$(( now - start )); fi ;;
    esac
    if [ -n "$secs" ] && [ "$secs" -ge 0 ] 2>/dev/null; then
      row="${row} $(fg "$DIM2")·${R} $(fg "$DIM")$(fdur "$secs")${R}"
    fi

    "$JQ" -nc --arg id "$id" --arg c "$row" '{id:$id, content:$c}'
  done
exit 0
