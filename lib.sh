# cockpit/lib.sh — shared helpers. Source this; don't execute.
# Three concerns: (1) pick sessions from santa-claude's DB, (2) map a session
# to its live JSONL transcript, (3) classify that transcript's current state.

SANTA_DB="${SANTA_DB:-$HOME/.local/share/santa-claude/index.db}"
PROJECTS_DIR="${PROJECTS_DIR:-$HOME/.claude/projects}"
COCKPIT_SESSION="${COCKPIT_SESSION:-cockpit}"
# Pane arrangement. even-horizontal = tall side-by-side columns (best on wide
# monitors); tiled = grid; even-vertical = stacked rows. Override via env.
COCKPIT_LAYOUT="${COCKPIT_LAYOUT:-even-horizontal}"

# --- session selection ------------------------------------------------------

# Encode a cwd to its ~/.claude/projects directory name (Claude replaces / and . with -)
encode_project_dir() { echo "$1" | sed 's#[/.]#-#g'; }

# Path to a session's JSONL transcript, given id + cwd.
session_jsonl() {
  local id="$1" cwd="$2"
  local enc; enc="$PROJECTS_DIR/$(encode_project_dir "$cwd")/$id.jsonl"
  [[ -f "$enc" ]] && { echo "$enc"; return; }
  # fallback: search by id across all project dirs
  local hit; hit=$(find "$PROJECTS_DIR" -maxdepth 2 -name "$id.jsonl" 2>/dev/null | head -1)
  echo "$hit"
}

# A session is "live" (running somewhere) if its transcript was written very
# recently. mtime is the reliable signal — Claude appends-and-closes, so lsof
# misses it. Live sessions must NOT be resumed (double-attach corrupts them);
# use wtfocus to jump to those instead.
COCKPIT_LIVE_SECS="${COCKPIT_LIVE_SECS:-150}"
session_is_live() {
  local jsonl="$1" now mtime
  [[ -f "$jsonl" ]] || return 1
  now=$(date +%s); mtime=$(stat -c %Y "$jsonl" 2>/dev/null || echo 0)
  (( now - mtime < COCKPIT_LIVE_SECS ))
}

# Process-based liveness: true if a `claude --resume <id>` is already running
# anywhere. mtime alone misses a live session that's been quiet longer than
# COCKPIT_LIVE_SECS (user reading/thinking/away) — picking it as a "dormant"
# candidate would double-resume it, churning the shared transcript (the source
# of the green/blue flicker) and risking corruption. This is the authoritative
# check; the mtime window stays as a backstop for non-resume launches.
session_is_running() {
  local id="$1"
  pgrep -f "claude --resume $id" >/dev/null 2>&1
}

# Emit candidate sessions for the grid: the most-recently-touched transcripts
# that are dormant (not live) and not marked completed. Recency comes from the
# filesystem (always current); status + label come from santa-claude's DB.
# Output TSV: id<TAB>cwd<TAB>title   (limit via $1, default 6)
COCKPIT_MAX_AGE_DAYS="${COCKPIT_MAX_AGE_DAYS:-30}"
cockpit_candidates() {
  local limit="${1:-6}" id title status cwd jsonl n=0
  local now cutoff; now=$(date +%s); cutoff=$(( now - COCKPIT_MAX_AGE_DAYS*86400 ))
  # one-shot DB snapshot: id -> status / title / cwd
  declare -A ST TL CW
  while IFS=$'\t' read -r id status title cwd; do
    ST[$id]="$status"; TL[$id]="$title"; CW[$id]="$cwd"
  done < <(sqlite3 -separator $'\t' "$SANTA_DB" \
    "SELECT id, status, coalesce(nullif(summary_title,''), substr(first_user_text,1,48), ''), coalesce(cwd,'') FROM sessions;")
  # walk transcripts newest-first
  while IFS= read -r jsonl; do
    id=$(basename "$jsonl" .jsonl)
    (( $(stat -c %Y "$jsonl" 2>/dev/null || echo 0) < cutoff )) && continue   # too old to be worth resuming
    [[ "${ST[$id]:-active}" == "completed" || "${ST[$id]:-}" == "archived" ]] && continue
    session_is_running "$id" && continue            # already attached elsewhere → skip (don't double-resume)
    session_is_live "$jsonl" && continue            # recently active → skip
    cwd="${CW[$id]:-}"
    [[ -z "$cwd" ]] && cwd=$(jq -r 'select(.cwd)|.cwd' "$jsonl" 2>/dev/null | head -1)
    [[ -z "$cwd" ]] && continue
    title="${TL[$id]:-}"
    if [[ -z "$title" ]]; then   # not summarised yet → use the opening user prompt
      title=$(jq -r 'select(.type=="user") | (.message.content | if type=="string" then . else (map(select(.type=="text").text)|join(" ")) end)' "$jsonl" 2>/dev/null \
              | grep -v '^$' | head -1 | tr '\n' ' ' | cut -c1-60 || true)
    fi
    [[ "$title" == "<<santa-claude-internal>>"* ]] && continue   # santa's own claude -p runs
    [[ -z "$title" ]] && title="(untitled)"
    printf '%s\t%s\t%s\n' "$id" "$cwd" "$title"
    n=$((n+1)); [[ $n -ge $limit ]] && break
  done < <(find "$PROJECTS_DIR" -maxdepth 2 -name '*.jsonl' -printf '%T@\t%p\n' 2>/dev/null \
            | sort -rn | cut -f2-)
}

# --- state classification ---------------------------------------------------
# Reads a transcript tail + mtime, prints one of: working|idle|needs-input|dead
# Tunables via env: COCKPIT_WORKING_SECS (default 4), COCKPIT_STALL_SECS (90)

# Classify by the LAST transcript event FIRST, using mtime only to distinguish
# an in-progress turn (file moving) from a stalled one. This ordering matters:
# `claude --resume` rewrites a transcript on load, bumping its mtime with no
# work happening. An mtime-first rule therefore paints every freshly-resumed
# pane "working", then latches it "just-finished" the moment it settles — the
# blue/grey churn you saw across all panes. The tail is authoritative: a turn
# that ended (end_turn) is idle no matter how fresh the file is.
classify_state() {
  local jsonl="$1"
  [[ -f "$jsonl" ]] || { echo dead; return; }
  local now mtime age last stop wants_tool has_result fresh=0
  now=$(date +%s); mtime=$(stat -c %Y "$jsonl" 2>/dev/null || echo 0)
  age=$(( now - mtime ))
  (( age < ${COCKPIT_WORKING_SECS:-4} )) && fresh=1
  last=$(tail -1 "$jsonl" 2>/dev/null)
  stop=$(jq -r '.message.stop_reason // ""' <<<"$last" 2>/dev/null)
  wants_tool=$(jq -r '[.message.content[]? | select(.type=="tool_use")] | length>0' <<<"$last" 2>/dev/null)
  has_result=$(jq -r 'if .type=="user" then ([.message.content[]? | select(.type=="tool_result")] | length>0) else false end' <<<"$last" 2>/dev/null)

  # finished turn — assistant is done, waiting on the human → idle (even if the
  # mtime is fresh from a resume rewrite).
  if [[ "$stop" == "end_turn" || "$stop" == "stop_sequence" ]]; then echo idle; return; fi
  # assistant requested a tool / permission with no result yet: file still
  # moving → working; gone quiet → blocked on the human.
  if [[ "$stop" == "tool_use" || "$wants_tool" == "true" ]] && [[ "$has_result" != "true" ]]; then
    (( fresh )) && echo working || echo needs-input; return
  fi
  # mid-stream (a tool result just landed, or a partial assistant message):
  # only call it working while the file is actively moving, else settled.
  (( fresh )) && echo working || echo idle
}

# Seconds since last transcript activity (for "idle 6m" labels)
idle_seconds() {
  local jsonl="$1" now mtime
  [[ -f "$jsonl" ]] || { echo 999999; return; }
  now=$(date +%s); mtime=$(stat -c %Y "$jsonl" 2>/dev/null || echo 0)
  echo $(( now - mtime ))
}
