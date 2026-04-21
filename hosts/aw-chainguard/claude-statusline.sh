#!/bin/bash
input=$(cat)

# --- Extract fields ---
MODEL=$(echo "$input" | jq -r '.model.display_name // "unknown"')
MODEL_ID=$(echo "$input" | jq -r '.model.id // ""')

# Context window
CTX_PCT=$(echo "$input" | jq -r '.context_window.used_percentage // 0' | cut -d. -f1)
CTX_SIZE=$(echo "$input" | jq -r '.context_window.context_window_size // 0')
IN_TOK=$(echo "$input" | jq -r '.context_window.total_input_tokens // 0')
OUT_TOK=$(echo "$input" | jq -r '.context_window.total_output_tokens // 0')

# Cost & duration
COST=$(echo "$input" | jq -r '.cost.total_cost_usd // 0')
DURATION_MS=$(echo "$input" | jq -r '.cost.total_duration_ms // 0')
API_MS=$(echo "$input" | jq -r '.cost.total_api_duration_ms // 0')
LINES_ADD=$(echo "$input" | jq -r '.cost.total_lines_added // 0')
LINES_DEL=$(echo "$input" | jq -r '.cost.total_lines_removed // 0')

# Rate limits (Pro/Max only — may be absent)
QUOTA_5H=$(echo "$input" | jq -r '.rate_limits.five_hour.used_percentage // empty')
QUOTA_5H_RESET=$(echo "$input" | jq -r '.rate_limits.five_hour.resets_at // empty')
QUOTA_7D=$(echo "$input" | jq -r '.rate_limits.seven_day.used_percentage // empty')
QUOTA_7D_RESET=$(echo "$input" | jq -r '.rate_limits.seven_day.resets_at // empty')

# --- Colors ---
RST='\033[0m'
BOLD='\033[1m'
DIM='\033[2m'
RED='\033[31m'
GRN='\033[32m'
YEL='\033[33m'
BLU='\033[34m'
MAG='\033[35m'
CYN='\033[36m'
WHT='\033[37m'
BGRY='\033[90m'  # bright gray

# --- Helpers ---
fmt_tokens() {
  local n=$1
  if [ "$n" -ge 1000000 ]; then
    printf "%.1fM" "$(echo "$n / 1000000" | bc -l)"
  elif [ "$n" -ge 1000 ]; then
    printf "%.1fk" "$(echo "$n / 1000" | bc -l)"
  else
    printf "%d" "$n"
  fi
}

fmt_duration() {
  local ms=$1
  local total_sec=$((ms / 1000))
  local h=$((total_sec / 3600))
  local m=$(( (total_sec % 3600) / 60 ))
  local s=$((total_sec % 60))
  if [ "$h" -gt 0 ]; then
    printf "%dh%02dm" "$h" "$m"
  elif [ "$m" -gt 0 ]; then
    printf "%dm%02ds" "$m" "$s"
  else
    printf "%ds" "$s"
  fi
}

progress_bar() {
  local pct=$1
  local width=${2:-12}
  local filled=$((pct * width / 100))
  [ "$filled" -gt "$width" ] && filled=$width
  local empty=$((width - filled))
  local bar=""

  # Color based on percentage
  local color="$GRN"
  [ "$pct" -ge 50 ] && color="$YEL"
  [ "$pct" -ge 75 ] && color="$RED"

  local fill_chars="" empty_chars=""
  [ "$filled" -gt 0 ] && printf -v fill_chars "%${filled}s" "" && fill_chars="${fill_chars// /━}"
  [ "$empty" -gt 0 ] && printf -v empty_chars "%${empty}s" "" && empty_chars="${empty_chars// /╌}"

  printf '%b' "${color}${fill_chars}${BGRY}${empty_chars}${RST}"
}

quota_bar() {
  local pct=$1
  local width=${2:-8}
  local int_pct=$(printf '%.0f' "$pct")
  local filled=$((int_pct * width / 100))
  [ "$filled" -gt "$width" ] && filled=$width
  local empty=$((width - filled))

  local color="$GRN"
  [ "$int_pct" -ge 50 ] && color="$YEL"
  [ "$int_pct" -ge 80 ] && color="$RED"

  local fill_chars="" empty_chars=""
  [ "$filled" -gt 0 ] && printf -v fill_chars "%${filled}s" "" && fill_chars="${fill_chars// /▮}"
  [ "$empty" -gt 0 ] && printf -v empty_chars "%${empty}s" "" && empty_chars="${empty_chars// /▯}"

  printf '%b' "${color}${fill_chars}${BGRY}${empty_chars}${RST}"
}

fmt_reset_time() {
  local reset_epoch=$1
  local now=$(date +%s)
  local diff=$((reset_epoch - now))
  if [ "$diff" -le 0 ]; then
    printf "now"
  elif [ "$diff" -lt 3600 ]; then
    printf "%dm" $((diff / 60))
  else
    printf "%dh%dm" $((diff / 3600)) $(( (diff % 3600) / 60 ))
  fi
}

# --- Format values ---
IN_FMT=$(fmt_tokens "$IN_TOK")
OUT_FMT=$(fmt_tokens "$OUT_TOK")
CTX_FMT=$(fmt_tokens "$CTX_SIZE")
COST_FMT=$(printf '$%.2f' "$COST")
DUR_FMT=$(fmt_duration "$DURATION_MS")
API_FMT=$(fmt_duration "$API_MS")

# --- Line 1: Model + Context ---
CTX_BAR=$(progress_bar "$CTX_PCT" 14)
printf '%b' "${BOLD}${CYN}${MODEL}${RST}"
printf '%b' " ${BGRY}|${RST} "
printf '%b' "ctx ${CTX_BAR} ${BOLD}${CTX_PCT}%%${RST}${DIM}/${CTX_FMT}${RST}"
echo ""

# --- Line 2: Tokens + Cost + Duration ---
printf '%b' "${BLU}in:${RST}${IN_FMT} ${MAG}out:${RST}${OUT_FMT}"
printf '%b' " ${BGRY}|${RST} "
printf '%b' "${YEL}${COST_FMT}${RST}"
printf '%b' " ${BGRY}|${RST} "
printf '%b' "${GRN}${DUR_FMT}${RST}${DIM}(api ${API_FMT})${RST}"

# Lines changed
if [ "$LINES_ADD" -gt 0 ] || [ "$LINES_DEL" -gt 0 ]; then
  printf '%b' " ${BGRY}|${RST} "
  [ "$LINES_ADD" -gt 0 ] && printf '%b' "${GRN}+${LINES_ADD}${RST}"
  [ "$LINES_ADD" -gt 0 ] && [ "$LINES_DEL" -gt 0 ] && printf '%b' "${DIM}/${RST}"
  [ "$LINES_DEL" -gt 0 ] && printf '%b' "${RED}-${LINES_DEL}${RST}"
fi
echo ""

# --- Line 3: Rate Limits (only if available) ---
if [ -n "$QUOTA_5H" ] || [ -n "$QUOTA_7D" ]; then
  printf '%b' "${BOLD}quota${RST} "
  if [ -n "$QUOTA_5H" ]; then
    Q5_INT=$(printf '%.0f' "$QUOTA_5H")
    Q5_BAR=$(quota_bar "$QUOTA_5H" 8)
    printf '%b' "5h ${Q5_BAR} ${Q5_INT}%%"
    if [ -n "$QUOTA_5H_RESET" ]; then
      RESET_5H=$(fmt_reset_time "$QUOTA_5H_RESET")
      printf '%b' "${DIM}(${RESET_5H})${RST}"
    fi
  fi
  if [ -n "$QUOTA_7D" ]; then
    [ -n "$QUOTA_5H" ] && printf '%b' " ${BGRY}|${RST} "
    Q7_INT=$(printf '%.0f' "$QUOTA_7D")
    Q7_BAR=$(quota_bar "$QUOTA_7D" 8)
    printf '%b' "7d ${Q7_BAR} ${Q7_INT}%%"
    if [ -n "$QUOTA_7D_RESET" ]; then
      RESET_7D=$(fmt_reset_time "$QUOTA_7D_RESET")
      printf '%b' "${DIM}(${RESET_7D})${RST}"
    fi
  fi
  echo ""
fi
