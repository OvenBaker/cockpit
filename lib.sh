# cockpit/lib.sh — shared helpers. Source this; don't execute.
# Three concerns: (1) pick sessions from santa-claude's DB, (2) map a session
# to its live JSONL transcript, (3) classify that transcript's current state.

SANTA_DB="${SANTA_DB:-$HOME/.local/share/santa-claude/index.db}"
PROJECTS_DIR="${PROJECTS_DIR:-$HOME/.claude/projects}"
CODEX_SESSIONS="${CODEX_SESSIONS:-$HOME/.codex/sessions}"   # Codex rollout store
COCKPIT_SESSION="${COCKPIT_SESSION:-cockpit}"
# Pane arrangement. even-horizontal = tall side-by-side columns (best on wide
# monitors); tiled = grid; even-vertical = stacked rows. Override via env.
COCKPIT_LAYOUT="${COCKPIT_LAYOUT:-even-horizontal}"

# Print a layout snapshot to stdout: an @active line naming the current
# workspace, then one window-grouped record per pane. <nil> placeholders keep
# empty fields from collapsing on read. Shared by the poller (autosave) and
# `cockpit --save`. Requires a running session.
cockpit_snapshot() {
  local tmux=${COCKPIT_TMUX:-"tmux -L cockpit"} TAB=$'\t' NIL='<nil>'
  printf '@active%s%s\n' "$TAB" "$($tmux display -p -t "$COCKPIT_SESSION" '#{window_name}' 2>/dev/null)"
  $tmux list-panes -s -t "$COCKPIT_SESSION" \
    -F "#{window_index}${TAB}#{window_name}${TAB}#{?@session_id,#{@session_id},$NIL}${TAB}#{?@cwd,#{@cwd},$NIL}${TAB}#{?@label,#{@label},$NIL}${TAB}#{?@agent,#{@agent},claude}" 2>/dev/null
}

# The window the user is currently viewing = the active workspace. Helpers that
# add/remove/retarget panes act on THIS window, not a hardcoded :0, so they work
# whichever workspace you're in. Falls back to :0 if nothing's resolvable.
cockpit_cur_window() {
  local tmux=${COCKPIT_TMUX:-"tmux -L cockpit"} w
  w=$($tmux display -p -t "$COCKPIT_SESSION" '#{window_id}' 2>/dev/null)
  echo "${w:-$COCKPIT_SESSION:0}"
}

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

# Candidate dormant Claude sessions, newest-first, as: mtime<TAB>claude<TAB>id<TAB>cwd<TAB>title
COCKPIT_MAX_AGE_DAYS="${COCKPIT_MAX_AGE_DAYS:-30}"
_claude_rows() {
  local limit="${1:-16}" id title status cwd jsonl mt n=0 headblk
  local now cutoff; now=$(date +%s); cutoff=$(( now - COCKPIT_MAX_AGE_DAYS*86400 ))
  declare -A ST TL CW
  while IFS=$'\t' read -r id status title cwd; do
    ST[$id]="$status"; TL[$id]="$title"; CW[$id]="$cwd"
  done < <(sqlite3 -separator $'\t' "$SANTA_DB" \
    "SELECT id, status, coalesce(nullif(summary_title,''), substr(first_user_text,1,80), ''), coalesce(cwd,'') FROM sessions;")
  while IFS=$'\t' read -r mt jsonl; do
    (( ${mt%.*} < cutoff )) && break                # sorted newest-first → rest are older
    id=$(basename "$jsonl" .jsonl)
    [[ "${ST[$id]:-active}" == "completed" || "${ST[$id]:-}" == "archived" ]] && continue
    session_is_running "$id" && continue
    session_is_live "$jsonl" && continue
    cwd="${CW[$id]:-}"; title="${TL[$id]:-}"
    if [[ -z "$cwd" || -z "$title" ]]; then
      # Not (fully) indexed by santa. Read only the head — cwd is on every event
      # line and the first user message is near the top — instead of parsing the
      # whole transcript. santa's own `claude -p` runs flood the recent-by-mtime
      # list; skip those cheaply here rather than full-file-jq'ing each to find out.
      headblk=$(head -n 80 "$jsonl" 2>/dev/null)
      [[ "$headblk" == *'<<santa-claude-internal>>'* ]] && continue
      [[ -z "$cwd" ]] && cwd=$(jq -r 'select(.cwd)|.cwd' <<<"$headblk" 2>/dev/null | head -1)
      [[ -z "$title" ]] && title=$(jq -r 'select(.type=="user") | (.message.content | if type=="string" then . else (map(select(.type=="text").text)|join(" ")) end)' <<<"$headblk" 2>/dev/null | grep -v '^$' | head -1 | tr '\n' ' ' | cut -c1-100 || true)
    fi
    [[ -z "$cwd" ]] && continue
    [[ "$title" == "<<santa-claude-internal>>"* ]] && continue
    [[ -z "$title" ]] && title="(untitled)"
    printf '%s\tclaude\t%s\t%s\t%s\n' "${mt%.*}" "$id" "$cwd" "$title"
    n=$((n+1)); (( n >= limit )) && break
  done < <(find "$PROJECTS_DIR" -maxdepth 2 -name '*.jsonl' -printf '%T@\t%p\n' 2>/dev/null | sort -rn)
}

# Candidate dormant Codex sessions, newest-first, as: mtime<TAB>codex<TAB>id<TAB>cwd<TAB>title
_codex_rows() {
  local limit="${1:-16}" f id cwd title mt n=0
  local now cutoff; now=$(date +%s); cutoff=$(( now - COCKPIT_MAX_AGE_DAYS*86400 ))
  [[ -d "$CODEX_SESSIONS" ]] || return 0
  # santa indexes codex sessions too — pull status/title/cwd from the DB (keyed by id)
  # so we don't full-file-jq every rollout for its title. No provider filter: keying by
  # the codex id is enough, and it works on a DB that predates the provider column.
  declare -A ST TL CW
  while IFS=$'\t' read -r id status title cwd; do
    ST[$id]="$status"; TL[$id]="$title"; CW[$id]="$cwd"
  done < <(sqlite3 -separator $'\t' "$SANTA_DB" \
    "SELECT id, status, coalesce(nullif(summary_title,''), substr(first_user_text,1,80), ''), coalesce(cwd,'') FROM sessions;")
  while IFS=$'\t' read -r mt f; do
    (( ${mt%.*} < cutoff )) && break
    id=$(basename "$f" | sed -E 's/.*-([0-9a-f-]{36})\.jsonl$/\1/'); [[ -n "$id" ]] || continue
    [[ "${ST[$id]:-active}" == "completed" || "${ST[$id]:-}" == "archived" ]] && continue
    session_is_running_agent codex "$id" && continue
    session_is_live "$f" && continue
    cwd="${CW[$id]:-}"
    [[ -z "$cwd" ]] && cwd=$(jq -r 'select(.type=="session_meta")|.payload.cwd // empty' <<<"$(head -1 "$f")" 2>/dev/null)
    [[ -n "$cwd" ]] || continue
    title="${TL[$id]:-}"
    # Fallback only for codex rollouts santa hasn't ingested yet: read the head, not
    # the whole transcript — the first user_message sits just after session_meta/turn_context.
    [[ -z "$title" ]] && title=$(head -n 40 "$f" 2>/dev/null | jq -rc 'select(.payload.type=="user_message")|.payload.message' 2>/dev/null | grep -v '^$' | head -1 | tr '\n' ' ' | cut -c1-100)
    [[ -n "$title" ]] || title="(codex ${id:0:8})"
    printf '%s\tcodex\t%s\t%s\t%s\n' "${mt%.*}" "$id" "$cwd" "$title"
    n=$((n+1)); (( n >= limit )) && break
  done < <(find "$CODEX_SESSIONS" -type f -name 'rollout-*.jsonl' -printf '%T@\t%p\n' 2>/dev/null | sort -rn)
}

# Merged candidate sessions across agents, most-recent first.
# Output TSV: id<TAB>cwd<TAB>title<TAB>agent   (limit via $1, default 6)
cockpit_candidates() {
  local limit="${1:-6}"
  { _claude_rows $((limit + 12)); _codex_rows $((limit + 12)); } \
    | sort -t$'\t' -k1,1 -rn | head -n "$limit" \
    | awk -F'\t' 'BEGIN{OFS="\t"}{print $3,$4,$5,$2}'   # mtime,agent,id,cwd,title → id,cwd,title,agent
}

# Distinct working directories you've worked in recently, newest first, filtered
# to ones that still exist — for the new-session picker (Alt-N). $1 = limit.
cockpit_recent_cwds() {
  local limit="${1:-15}" d
  sqlite3 "$SANTA_DB" \
    "SELECT cwd FROM sessions WHERE coalesce(cwd,'')<>'' \
     GROUP BY cwd ORDER BY MAX(coalesce(last_active_at, ended_at, started_at)) DESC \
     LIMIT $((limit*2));" 2>/dev/null \
  | while IFS= read -r d; do [[ -d "$d" ]] && echo "$d"; done | head -n "$limit"
}

# Adopt a just-started session: the newest transcript under <cwd>'s project dir
# modified after <born-epoch>. Used by the poller to bind a freshly-spawned
# `claude` (no --resume, so no id up front) to its pane once Claude writes.
cockpit_adopt() {
  local cwd="$1" born="$2" dir f
  dir="$PROJECTS_DIR/$(encode_project_dir "$cwd")"
  [[ -d "$dir" ]] || return 0
  f=$(find "$dir" -maxdepth 1 -name '*.jsonl' -newermt "@$born" -printf '%T@\t%p\n' 2>/dev/null \
      | sort -rn | head -1 | cut -f2)
  [[ -n "$f" ]] && basename "$f" .jsonl
}

# --- state classification ---------------------------------------------------
# Reads a transcript tail + mtime, prints one of: working|idle|needs-input|dead
# Tunables via env: COCKPIT_WORKING_SECS (working window, default 12),
# COCKPIT_NEEDS_SECS (quiet-on-a-pending-tool before "needs-input", default 25)

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
  local now mtime age last stop wants_tool has_result
  now=$(date +%s); mtime=$(stat -c %Y "$jsonl" 2>/dev/null || echo 0)
  age=$(( now - mtime ))
  last=$(tail -1 "$jsonl" 2>/dev/null)
  stop=$(jq -r '.message.stop_reason // ""' <<<"$last" 2>/dev/null)
  wants_tool=$(jq -r '[.message.content[]? | select(.type=="tool_use")] | length>0' <<<"$last" 2>/dev/null)
  has_result=$(jq -r 'if .type=="user" then ([.message.content[]? | select(.type=="tool_result")] | length>0) else false end' <<<"$last" 2>/dev/null)

  # finished turn — assistant is done, waiting on the human → idle (even if the
  # mtime is fresh from a resume rewrite).
  if [[ "$stop" == "end_turn" || "$stop" == "stop_sequence" ]]; then echo idle; return; fi
  # tool/permission requested, no result yet: a turn IS in progress. Stay
  # 'working' through normal tool runs and thinking pauses; only flag
  # needs-input once it's been quiet long enough to look genuinely blocked.
  if [[ "$stop" == "tool_use" || "$wants_tool" == "true" ]] && [[ "$has_result" != "true" ]]; then
    (( age >= ${COCKPIT_NEEDS_SECS:-25} )) && echo needs-input || echo working
    return
  fi
  # mid-turn (partial message / tool result just landed): 'working' while
  # recently written, else settled. The window is wide (COCKPIT_WORKING_SECS)
  # so streaming/thinking gaps don't flip green↔blue on an active pane.
  (( age < ${COCKPIT_WORKING_SECS:-12} )) && echo working || echo idle
}

# Seconds since last transcript activity (for "idle 6m" labels)
idle_seconds() {
  local jsonl="$1" now mtime
  [[ -f "$jsonl" ]] || { echo 999999; return; }
  now=$(date +%s); mtime=$(stat -c %Y "$jsonl" 2>/dev/null || echo 0)
  echo $(( now - mtime ))
}

# --- multi-agent provider layer (claude | codex) ----------------------------
# Each pane carries @agent; these dispatch transcript-location, classification
# and the resume command per provider so cockpit handles both side by side.

# Codex rollout for a session id: ~/.codex/sessions/YYYY/MM/DD/rollout-…-<uuid>.jsonl
codex_transcript() { find "$CODEX_SESSIONS" -type f -name "*-$1.jsonl" 2>/dev/null | head -1; }

# Transcript file for (agent, id, cwd).
agent_transcript() {
  case "$1" in codex) codex_transcript "$2";; *) session_jsonl "$2" "$3";; esac
}

# Classify a Codex rollout's live state. Last event_msg/task_complete = idle;
# a response_item/function_call awaiting output that's gone quiet = needs-input;
# fresh file = working.
# Codex live state from the most-recent TURN BOUNDARY, not the last raw event.
# A `task_started` with no following `task_complete` means a turn is in progress
# — running a tool (even a long `sleep`), searching, or thinking → working.
# `task_complete` / `turn_aborted` → idle. Codex (auto-approve) emits no
# "awaiting approval" event, so there's no reliable needs-input signal and we
# never flag it — a long pending function_call is a running tool, not a block.
classify_codex() {
  local j="$1" now mtime age lt
  [[ -f "$j" ]] || { echo dead; return; }
  now=$(date +%s); mtime=$(stat -c %Y "$j" 2>/dev/null || echo 0); age=$(( now - mtime ))
  lt=$(tac "$j" 2>/dev/null | grep -m1 -oE '"(task_started|task_complete|turn_aborted)"' | tr -d '"') || true
  case "$lt" in
    task_started)               echo working; return;;
    task_complete|turn_aborted) echo idle; return;;
  esac
  (( age < ${COCKPIT_WORKING_SECS:-12} )) && echo working || echo idle   # no turn boundary yet
}

agent_classify() { case "$1" in codex) classify_codex "$2";; *) classify_state "$2";; esac; }

# Inner shell command to (re)launch a pane for (agent, id, cwd).
agent_resume_inner() {
  case "$1" in
    codex) printf 'cd %q && exec codex resume %s' "$3" "$2";;
    *)     printf 'cd %q && exec claude --resume %s' "$3" "$2";;
  esac
}

# Is a session already running? (don't double-resume). claude has a per-id
# process; codex's resumed process carries `resume <id>` in its argv too.
session_is_running_agent() {
  case "$1" in
    codex) pgrep -f "resume $2" >/dev/null 2>&1;;
    *)     session_is_running "$2";;
  esac
}

# Which agent owns a given session id? (for cockpit-send / related handoff)
agent_of_id() {
  [[ -n "$(codex_transcript "$1")" ]] && { echo codex; return; }
  echo claude
}

# Adopt a freshly-started session (no id up front) for a pane, per agent.
# claude: newest transcript under the cwd's project dir born after the pane.
# codex:  newest rollout overall born after the pane whose session_meta cwd matches.
cockpit_adopt_agent() {
  local agent="$1" cwd="$2" born="$3"
  if [[ "$agent" == codex ]]; then
    local f
    while IFS= read -r f; do
      [[ "$(jq -r 'select(.type=="session_meta")|.payload.cwd // empty' <<<"$(head -1 "$f")" 2>/dev/null)" == "$cwd" ]] || continue
      basename "$f" | sed -E 's/.*-([0-9a-f-]{36})\.jsonl$/\1/'; return
    done < <(find "$CODEX_SESSIONS" -type f -name 'rollout-*.jsonl' -newermt "@$born" -printf '%T@\t%p\n' 2>/dev/null | sort -rn | cut -f2-)
    return 0
  fi
  cockpit_adopt "$cwd" "$born"
}
