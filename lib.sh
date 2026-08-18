# cockpit/lib.sh — shared helpers. Source this; don't execute.
# Three concerns: (1) pick sessions from santa's DB, (2) map a session
# to its live JSONL transcript, (3) classify that transcript's current state.

SANTA_DB="${SANTA_DB:-$HOME/.local/share/santa/index.db}"
PROJECTS_DIR="${PROJECTS_DIR:-$HOME/.claude/projects}"
CODEX_SESSIONS="${CODEX_SESSIONS:-$HOME/.codex/sessions}"   # Codex rollout store
COCKPIT_SESSION="${COCKPIT_SESSION:-cockpit}"
# Pane arrangement. even-horizontal = tall side-by-side columns (best on wide
# monitors); tiled = grid; even-vertical = stacked rows. Override via env.
COCKPIT_LAYOUT="${COCKPIT_LAYOUT:-even-horizontal}"

# Remote Control: launch claude panes with `--remote-control` so they can be
# driven from the Claude app (claude.ai / mobile) — the whole point of steering
# cockpit away from the desk. On by default; set COCKPIT_REMOTE_CONTROL=0 for
# plain interactive panes. Codex has no remote-control equivalent, so this only
# affects claude launches.
COCKPIT_REMOTE_CONTROL="${COCKPIT_REMOTE_CONTROL:-1}"

# Emit the ` --remote-control <name>` fragment for a claude launch — leading
# space, empty string when RC is off. <name> is what the Claude app shows in its
# session list; defaults to the cwd basename (trimmed) when a caller has no nicer
# label. %q-quoted so it survives intact through the `bash -lc "$(printf %q …)"`
# wrapper every launcher builds around the resume/spawn command (one unquote by
# that inner bash restores the literal name — same proven path as `cd %q`).
cockpit_rc_args() {
  [[ "${COCKPIT_REMOTE_CONTROL:-1}" == 1 ]] || return 0
  local name="${1:-}" cwd="${2:-}"
  [[ -n "$name" ]] || name=$(basename "${cwd:-$HOME}")
  printf ' --remote-control %q' "${name:0:48}"
}

# Cockpit's Codex permission posture. Cockpit sessions deliberately run without
# the Linux/WSL bwrap sandbox, while keeping on-request approvals routed through
# Codex's automatic reviewer. Keep this as one argv source so new, resumed,
# seeded, and brief-studio launches cannot drift apart.
cockpit_codex_launch_argv() {
  printf '%s\n' \
    --sandbox danger-full-access \
    --ask-for-approval on-request \
    -c approvals_reviewer=auto_review
}

# The same argv, %q-quoted for launchers that cross one bash -lc hop.
cockpit_codex_launch_args() {
  local arg
  while IFS= read -r arg; do printf ' %q' "$arg"; done < <(cockpit_codex_launch_argv)
}

# ── brief-studio launch posture (Orbital's `orb` MCP channel) ───────────────────────────────────────────────
# A brief-studio pane is a workshop session Orbital launched to develop a brief. Three things make it usable
# from Orbital and NOTHING else about the spawn contract changes:
#   1. provider STARTUP trust gates are suppressed — folder trust and new-MCP-server confirmation carry no
#      decision worth the operator's attention and block a seeded launch before any work begins;
#   2. the appended system text tells the worker to ask through `orb_ask` and end its turn;
#   3. the `orb` MCP server is provisioned into the session so it HAS that tool.
# All three are strictly PER INVOCATION: no user-level provider settings file is read or written, so nothing
# here follows the operator into his own sessions and nothing goes stale when a repository moves.

# The brief-studio system text. The brief is being AUTHORED here, so this deliberately carries none of the
# agent profile's three wrong clauses: no scope ceiling, no agent counterpart, no assume-and-proceed.
COCKPIT_BRIEF_STUDIO_SYSTEM_TEXT='You are developing a brief in the build-a-brief workshop. The brief is being AUTHORED here, not consumed: it is not complete and it is not a scope ceiling. Your counterpart is the operator, a person, reached in this terminal or through Orbital — never another agent. The loop exists to resolve ambiguity WITH him, so when a genuine ambiguity blocks good work do not state an assumption and proceed. Instead call the orb_ask tool: the question in plain text, and where a real choice exists 2 or 3 candidate readings, each with the consequence of taking it. Then END YOUR TURN immediately. orb_ask registers the question and returns a questionId at once. It never blocks, so do not wait on it, do not poll it, and do not hold any tool call open waiting for an answer. When the operator answers, your pane is woken with a notice naming that questionId; call orb_fetch once with it to collect his prose and his selected reading, then carry on. Do not use option menus and do not enter plan mode. Ingest the intake/ notes in the brief directory before asking a question or finishing a loop pass.'

# Read and validate an orb server specification file: {"command": "<abs>", "args": [...], "env": {...}}.
# Echoes the compact JSON on success; on failure prints nothing and returns 1, so every caller can refuse the
# launch LOUDLY naming the missing server rather than spawning a session that cannot ask anything.
cockpit_orb_read_spec() {
  local file="$1" json
  [[ "$file" == /* && ! -L "$file" && -f "$file" ]] || return 1
  json=$(jq -c '
    if (.command|type)!="string" or ((.command|startswith("/"))|not) then error("command")
    elif ((.args // []) | type)!="array" or ((.args // []) | length) > 16 then error("args")
    elif ((.args // []) | map(select(type!="string")) | length) > 0 then error("args")
    elif ((.env // {}) | type)!="object" or ((.env // {}) | length) > 16 then error("env")
    elif ((.env // {}) | to_entries | map(select((.key|test("^[A-Za-z_][A-Za-z0-9_]*$")|not) or (.value|type)!="string")) | length) > 0 then error("env")
    else {command: .command, args: (.args // []), env: (.env // {})} end' -- "$file" 2>/dev/null) || return 1
  [[ -x "$(jq -r .command <<<"$json")" ]] || return 1
  printf '%s' "$json"
}

# The ONE Codex trust override, as a raw `-c` VALUE (no shell quoting). Trust is asserted from the launch cwd
# AT INVOCATION TIME rather than persisted into ~/.codex/config.toml: Codex does NOT inherit trust from a parent
# entry (operator-verified), and a persisted absolute path would silently stall every Codex session the moment
# the repo moves. A cwd that cannot be expressed as a TOML basic key is refused, never quietly dropped.
# Callers that cross a shell hop quote the result themselves; a real-argv caller uses it verbatim.
cockpit_codex_trust_value() {
  local cwd="$1"
  case "$cwd" in *'"'*|*'\'*|*$'\n'*|*$'\t'*|*$'\r'*) return 1;; esac
  printf 'projects."%s".trust_level="trusted"' "$cwd"
}

# Codex per-invocation config VALUES for a brief-studio launch, one TOML assignment per line. JSON string
# escaping is also valid for TOML basic strings over this input charset. Keeping raw values here lets the plain
# spawn quote them for its one shell hop while cockpit-seed-exec passes them as a real argv array.
cockpit_codex_brief_studio_values() {
  local cwd="$1" orb="$2" value trust instructions
  trust=$(cockpit_codex_trust_value "$cwd") || return 1
  [[ -n "$orb" ]] || return 1
  printf '%s\n' "$trust"
  printf 'mcp_servers.orb.command=%s\n' "$(jq -r '.command|@json' <<<"$orb")"
  printf 'mcp_servers.orb.args=%s\n' "$(jq -c '.args' <<<"$orb")"
  # A TOML inline table of bare keys. The env values are our own paths and tokens, and jq's @json escaping is
  # exactly TOML basic-string escaping over that charset.
  value=$(jq -r '.env | to_entries | map(.key + "=" + (.value|@json)) | join(",")' <<<"$orb")
  printf 'mcp_servers.orb.env={%s}\n' "$value"
  # A valid spec is not enough: a brief session without its question channel must fail startup rather than
  # silently continue. Codex owns the runtime initialization verdict; `required` makes that verdict fatal.
  printf 'mcp_servers.orb.required=true\n'
  instructions=$(jq -rn --arg value "$COCKPIT_BRIEF_STUDIO_SYSTEM_TEXT" '$value|@json')
  printf 'developer_instructions=%s\n' "$instructions"
}

# ── execution launch posture (Orbital's `orb` channel on a WORK session) ───────────────────────────────────
# An `execution` pane is a piece of work Orbital launched, not a workshop authoring a brief. It gets the orb
# channel and NOTHING else: no permission-mode change, no tool removal, no appended system text. brief-studio
# is deliberately not reused — its bypassPermissions would silently escalate an execution, and its workshop
# instructions would tell a work session it is authoring a brief.
#
# What the session is told about the channel is Orbital's own launch prompt's job, not this profile's: the
# profile stays text-free so it cannot misdescribe the work it carries.
#
# Codex per-invocation config VALUES for an execution launch, one TOML assignment per line: the startup trust
# override (a pane sitting on a folder-trust dialog is a session nobody can answer) and the orb server. No
# developer_instructions — that is the line between this profile and brief-studio.
cockpit_codex_execution_values() {
  local cwd="$1" orb="$2" value trust
  trust=$(cockpit_codex_trust_value "$cwd") || return 1
  [[ -n "$orb" ]] || return 1
  printf '%s\n' "$trust"
  printf 'mcp_servers.orb.command=%s\n' "$(jq -r '.command|@json' <<<"$orb")"
  printf 'mcp_servers.orb.args=%s\n' "$(jq -c '.args' <<<"$orb")"
  value=$(jq -r '.env | to_entries | map(.key + "=" + (.value|@json)) | join(",")' <<<"$orb")
  printf 'mcp_servers.orb.env={%s}\n' "$value"
  # An execution launched to ask through orb, without orb, goes quiet on the operator with no way to say why.
  printf 'mcp_servers.orb.required=true\n'
}

# The same values, %q-quoted for the one `bash -lc` hop used by a plain Codex spawn.
cockpit_codex_brief_studio_args() {
  local values value
  values=$(cockpit_codex_brief_studio_values "$1" "$2") || return 1
  while IFS= read -r value; do printf ' -c %q' "$value"; done <<<"$values"
}

# Labels come from arbitrary user text (santa's first_user_text = a session's
# first prompt), which routinely contains newlines and tabs. Stored raw in
# @label those break two things: the single-line pane border, and — worse — the
# TAB-separated layout snapshot, where one embedded newline splits a pane across
# several physical rows. On restore that yields a row with an empty workspace
# name, and `NAMEWIN[""]` is a "bad array subscript" crash. Collapse every
# whitespace run to one space and trim, so a stored @label is always one clean
# line. Call this at EVERY @label setter.
ck_clean_label() {
  local s="$1"
  s="${s//$'\t'/ }"; s="${s//$'\r'/ }"; s="${s//$'\n'/ }"
  while [[ "$s" == *"  "* ]]; do s="${s//  / }"; done   # squeeze (≤80 chars, cheap)
  s="${s#"${s%%[![:space:]]*}"}"; s="${s%"${s##*[![:space:]]}"}"   # trim ends
  printf '%s' "$s"
}

# Print a layout snapshot to stdout: an @active line naming the current
# workspace, then one window-grouped record per pane. <nil> placeholders keep
# empty fields from collapsing on read. Shared by the poller (autosave) and
# `cockpit --save`. Requires a running session.
#
# The `-f` filter EXCLUDES orderly panes (@orderly set: brief/alpha/bravo). Those
# belong to orderlies-up, which owns the "aide" workspace's lifecycle and recreates
# it on cockpit attach. If we saved them, restore would rebuild a bare "aide" window
# (orderly panes carry no @session_id, so they resume as a plain shell), and then
# orderlies-up would open a SECOND "aide" — splitting the fleet across two tabs.
cockpit_snapshot() {
  local tmux=${COCKPIT_TMUX:-"tmux -L cockpit"} TAB=$'\t' NIL='<nil>'
  printf '@active%s%s\n' "$TAB" "$($tmux display -p -t "$COCKPIT_SESSION" '#{window_name}' 2>/dev/null)"
  $tmux list-panes -s -t "$COCKPIT_SESSION" -f '#{==:#{@orderly},}' \
    -F "#{window_index}${TAB}#{window_name}${TAB}#{?@session_id,#{@session_id},$NIL}${TAB}#{?@cwd,#{@cwd},$NIL}${TAB}#{?@label,#{@label},$NIL}${TAB}#{?@agent,#{@agent},$NIL}${TAB}#{?@cockpit_workspace_ref,#{@cockpit_workspace_ref},$NIL}${TAB}#{?@cockpit_pane_ref,#{@cockpit_pane_ref},$NIL}${TAB}#{?@cockpit_pane_generation,#{@cockpit_pane_generation},$NIL}${TAB}#{?@cockpit_pane_version,#{@cockpit_pane_version},$NIL}${TAB}#{?@cockpit_badge,#{@cockpit_badge},$NIL}" 2>/dev/null |
    awk -F "$TAB" -v nil="$NIL" '
      $3 != nil && ($6 == "claude" || $6 == "codex") {
        key=$6 SUBSEP $3; if (seen[key]++) next
      }
      { print }
    '
}

# --- layout store (SQLite) --------------------------------------------------
# The layout used to be a single TSV that the poller rewrote in place every few
# seconds. That made the CURRENT grid the only grid we knew about: when a
# restore dropped a workspace, the next autosave — five seconds later — wrote
# the loss over the only evidence of it, so there was nothing left to recover
# from or even diagnose against. Snapshots are now append-only rows, so every
# save keeps its predecessors and a bad restore can be diffed and undone.
# sqlite3 is already a hard dependency (santa's index, above).
COCKPIT_LAYOUT_DB="${COCKPIT_LAYOUT_DB:-${XDG_STATE_HOME:-$HOME/.local/state}/cockpit/layout.db}"
COCKPIT_LAYOUT_KEEP="${COCKPIT_LAYOUT_KEEP:-200}"   # snapshots retained per session

# SQL string literal from arbitrary text — the ONLY quoting rule that matters is
# doubling single quotes. Everything else (tabs, newlines, backslashes) is
# literal inside a SQLite string, which is precisely why storage stopped being
# the fragile part: no delimiter can be confused with content.
ck_sqesc() { local s="${1//\'/\'\'}"; printf "'%s'" "$s"; }

cockpit_layout_init() {
  mkdir -p "$(dirname "$COCKPIT_LAYOUT_DB")" 2>/dev/null || true
  # stdout is silenced deliberately: PRAGMA journal_mode echoes "wal", and this
  # runs inside command substitutions that would otherwise swallow it as data.
  sqlite3 "$COCKPIT_LAYOUT_DB" >/dev/null 2>&1 <<'SQL'
PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS snapshots(
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  at      INTEGER NOT NULL,
  session TEXT    NOT NULL,
  active  TEXT    NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS panes(
  snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  win         TEXT    NOT NULL DEFAULT '',
  workspace   TEXT    NOT NULL DEFAULT '',
  session_id  TEXT    NOT NULL DEFAULT '',
  cwd         TEXT    NOT NULL DEFAULT '',
  label       TEXT    NOT NULL DEFAULT '',
  agent       TEXT    NOT NULL DEFAULT '',
  workspace_ref  TEXT NOT NULL DEFAULT '',
  pane_ref       TEXT NOT NULL DEFAULT '',
  pane_generation TEXT NOT NULL DEFAULT '',
  pane_version    TEXT NOT NULL DEFAULT '',
  badge           TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(snapshot_id, seq));
CREATE INDEX IF NOT EXISTS idx_snapshots_session_at ON snapshots(session, at DESC);
SQL
  # CREATE TABLE IF NOT EXISTS does not add columns to an existing layout DB.
  # Each ALTER is race-tolerant: another poller may win between the probe and
  # the write, in which case the final probe is the authority.
  local name spec
  while IFS='|' read -r name spec; do
    [[ "$(sqlite3 "$COCKPIT_LAYOUT_DB" "SELECT count(*) FROM pragma_table_info('panes') WHERE name='$name';" 2>/dev/null)" == 1 ]] && continue
    sqlite3 "$COCKPIT_LAYOUT_DB" "ALTER TABLE panes ADD COLUMN $name $spec;" >/dev/null 2>&1 \
      || [[ "$(sqlite3 "$COCKPIT_LAYOUT_DB" "SELECT count(*) FROM pragma_table_info('panes') WHERE name='$name';" 2>/dev/null)" == 1 ]] \
      || return 1
  done <<'COLUMNS'
workspace_ref|TEXT NOT NULL DEFAULT ''
pane_ref|TEXT NOT NULL DEFAULT ''
pane_generation|TEXT NOT NULL DEFAULT ''
pane_version|TEXT NOT NULL DEFAULT ''
badge|TEXT NOT NULL DEFAULT ''
COLUMNS
}

# Persist a snapshot (the TSV text cockpit_snapshot prints) as one transaction.
# Older snapshots beyond COCKPIT_LAYOUT_KEEP are pruned here, so the history is
# bounded without a separate sweep.
cockpit_layout_save() {
  local snap="$1" sess="${COCKPIT_SESSION}" NIL='<nil>' sql active="" seq=0
  local f1 f2 f3 f4 f5 f6 f7 f8 f9 f10 f11
  [[ -n "$snap" ]] || return 1
  cockpit_layout_init
  sql="BEGIN IMMEDIATE;"
  while IFS=$'\t' read -r f1 f2 f3 f4 f5 f6 f7 f8 f9 f10 f11; do
    [[ "$f1" == "@active" ]] && { active="$f2"; continue; }
    [[ -n "$f1" ]] || continue
    [[ "$f3" == "$NIL" ]] && f3=""; [[ "$f4" == "$NIL" ]] && f4=""
    [[ "$f5" == "$NIL" ]] && f5=""; [[ "$f6" == "$NIL" ]] && f6=""
    [[ "$f7" == "$NIL" ]] && f7=""; [[ "$f8" == "$NIL" ]] && f8=""
    [[ "$f9" == "$NIL" ]] && f9=""; [[ "$f10" == "$NIL" ]] && f10=""
    [[ "$f11" == "$NIL" ]] && f11=""
    sql+="INSERT INTO panes(snapshot_id,seq,win,workspace,session_id,cwd,label,agent,workspace_ref,pane_ref,pane_generation,pane_version,badge) VALUES("
    sql+="(SELECT MAX(id) FROM snapshots),$seq,$(ck_sqesc "$f1"),$(ck_sqesc "$f2"),"
    sql+="$(ck_sqesc "$f3"),$(ck_sqesc "$f4"),$(ck_sqesc "$f5"),$(ck_sqesc "$f6"),"
    sql+="$(ck_sqesc "$f7"),$(ck_sqesc "$f8"),$(ck_sqesc "$f9"),$(ck_sqesc "$f10"),$(ck_sqesc "$f11"));"
    seq=$((seq+1))
  done <<<"$snap"
  (( seq > 0 )) || return 1        # never persist an empty grid over a good one
  sql="BEGIN IMMEDIATE;INSERT INTO snapshots(at,session,active) VALUES($(date +%s),$(ck_sqesc "$sess"),$(ck_sqesc "$active"));${sql#BEGIN IMMEDIATE;}"
  sql+="DELETE FROM panes WHERE snapshot_id IN (SELECT id FROM snapshots WHERE session=$(ck_sqesc "$sess") ORDER BY at DESC, id DESC LIMIT -1 OFFSET $COCKPIT_LAYOUT_KEEP);"
  sql+="DELETE FROM snapshots WHERE session=$(ck_sqesc "$sess") AND id NOT IN (SELECT id FROM snapshots WHERE session=$(ck_sqesc "$sess") ORDER BY at DESC, id DESC LIMIT $COCKPIT_LAYOUT_KEEP);"
  sql+="COMMIT;"
  printf '%s' "$sql" | sqlite3 "$COCKPIT_LAYOUT_DB" 2>/dev/null
}

# Print a stored snapshot back in cockpit_snapshot's TSV form, newest first by
# default; pass an offset (1 = the one before it) to walk back through history.
# Emitting the same shape the poller captures keeps restore's parser the single
# reader of that format.
cockpit_layout_load() {
  local back="${1:-0}" sess="${COCKPIT_SESSION}" sid
  [[ -f "$COCKPIT_LAYOUT_DB" ]] || return 1
  sid=$(sqlite3 "$COCKPIT_LAYOUT_DB" \
    "SELECT id FROM snapshots WHERE session=$(ck_sqesc "$sess") ORDER BY at DESC, id DESC LIMIT 1 OFFSET $back;" 2>/dev/null)
  [[ -n "$sid" ]] || return 1
  cockpit_layout_emit "$sid"
}

# Same, addressed by the snapshot id `cockpit --history` prints. An OFFSET is a
# moving target — the poller appends while you are deciding, so the "3 back" you
# read is not the "3 back" you restore. The id never moves, which is what you
# want when the snapshot you are reaching for is the one good grid in the list.
cockpit_layout_load_id() {
  local id="${1:-}" sess="${COCKPIT_SESSION}" sid
  [[ -f "$COCKPIT_LAYOUT_DB" && "$id" =~ ^[0-9]+$ ]] || return 1
  sid=$(sqlite3 "$COCKPIT_LAYOUT_DB" \
    "SELECT id FROM snapshots WHERE id=$id AND session=$(ck_sqesc "$sess");" 2>/dev/null)
  [[ -n "$sid" ]] || return 1
  cockpit_layout_emit "$sid"
}

# Shared row emitter for both addressing modes above. $1 is a snapshot id that
# the caller has already resolved (and confirmed belongs to this session).
cockpit_layout_emit() {
  local sid="$1" TAB=$'\t' NIL='<nil>'
  sqlite3 -separator "$TAB" "$COCKPIT_LAYOUT_DB" \
    "SELECT '@active', active FROM snapshots WHERE id=$sid;" 2>/dev/null
  sqlite3 -separator "$TAB" "$COCKPIT_LAYOUT_DB" \
    "SELECT win, workspace,
            CASE WHEN session_id='' THEN '$NIL' ELSE session_id END,
            CASE WHEN cwd='' THEN '$NIL' ELSE cwd END,
            CASE WHEN label='' THEN '$NIL' ELSE label END,
            CASE WHEN agent='' THEN '$NIL' ELSE agent END,
            CASE WHEN workspace_ref='' THEN '$NIL' ELSE workspace_ref END,
            CASE WHEN pane_ref='' THEN '$NIL' ELSE pane_ref END,
            CASE WHEN pane_generation='' THEN '$NIL' ELSE pane_generation END,
            CASE WHEN pane_version='' THEN '$NIL' ELSE pane_version END,
            CASE WHEN badge='' THEN '$NIL' ELSE badge END
     FROM panes WHERE snapshot_id=$sid ORDER BY seq;" 2>/dev/null
}

# "id<TAB>workspaces<TAB>panes" for the snapshot at offset $1 (0 = newest).
cockpit_layout_stats() {
  local back="${1:-0}" sess="${COCKPIT_SESSION}" TAB=$'\t'
  [[ -f "$COCKPIT_LAYOUT_DB" ]] || return 1
  sqlite3 -separator "$TAB" "$COCKPIT_LAYOUT_DB" \
    "SELECT s.id, COUNT(DISTINCT p.workspace), COUNT(p.seq)
     FROM snapshots s LEFT JOIN panes p ON p.snapshot_id=s.id
     WHERE s.session=$(ck_sqesc "$sess")
     GROUP BY s.id ORDER BY s.at DESC, s.id DESC LIMIT 1 OFFSET $back;" 2>/dev/null
}

# Same shape, for the RICHEST of the last $1 snapshots (most workspaces, then most
# panes). Restore uses this to notice that the layout it is about to rebuild is a
# fraction of a grid that existed an hour ago — the shape of a mass-close or a
# half-failed restore, which is otherwise invisible until the workspaces are gone.
cockpit_layout_peak() {
  local lim="${1:-30}" sess="${COCKPIT_SESSION}" TAB=$'\t'
  [[ -f "$COCKPIT_LAYOUT_DB" ]] || return 1
  sqlite3 -separator "$TAB" "$COCKPIT_LAYOUT_DB" \
    "SELECT id, ws, panes FROM (
       SELECT s.id AS id, COUNT(DISTINCT p.workspace) AS ws, COUNT(p.seq) AS panes, s.at AS at
       FROM snapshots s LEFT JOIN panes p ON p.snapshot_id=s.id
       WHERE s.session=$(ck_sqesc "$sess")
       GROUP BY s.id ORDER BY s.at DESC, s.id DESC LIMIT $lim)
     ORDER BY ws DESC, panes DESC, id DESC LIMIT 1;" 2>/dev/null
}

# One-time adoption of the pre-SQLite TSV so an upgrade doesn't start blind.
cockpit_layout_import_legacy() {
  local tsv="${1:-}" n
  [[ -s "$tsv" ]] || return 0
  cockpit_layout_init
  n=$(sqlite3 "$COCKPIT_LAYOUT_DB" "SELECT COUNT(*) FROM snapshots WHERE session=$(ck_sqesc "$COCKPIT_SESSION");" 2>/dev/null)
  [[ "${n:-0}" == 0 ]] || return 0
  cockpit_layout_save "$(cat "$tsv")" && echo "cockpit: imported legacy layout from $tsv" >&2
}

# The window the user is currently viewing = the active workspace. Helpers that
# add/remove/retarget panes act on THIS window, not a hardcoded :0, so they work
# whichever workspace you're in. Falls back to :0 if nothing's resolvable.
cockpit_cur_window() {
  local tmux=${COCKPIT_TMUX:-"tmux -L cockpit"} w
  w=$($tmux display -p -t "$COCKPIT_SESSION" '#{window_id}' 2>/dev/null)
  echo "${w:-$COCKPIT_SESSION:0}"
}

# --- soft-close bookkeeping -------------------------------------------------
# How many workspaces would SURVIVE the closes already in flight. A window marked
# @closing is still a live tmux window for the whole grace period, and a window
# marked @graveyard is a holding pen for a removed pane, not a workspace — so
# counting raw `list-windows` overstates what's left. That overstatement is what
# made "can't close the last workspace" a guard in name only: hold Alt-W down and
# every press sees all 24 windows still standing, marks another one, and moves on,
# until the whole grid is counting down at once (2026-08-12: 23 lost that way).
cockpit_ws_open_count() {
  local tmux=${COCKPIT_TMUX:-"tmux -L cockpit"} n=0 closing graveyard
  while IFS=$'\t' read -r closing graveyard; do
    [[ -z "$closing" && -z "$graveyard" ]] && n=$((n+1))
  done < <($tmux list-windows -t "$COCKPIT_SESSION" -F "#{@closing}"$'\t'"#{@graveyard}" 2>/dev/null)
  printf '%s' "$n"
}

# Pending-close undo STACK ("window_id:token" entries, newest last). This was a
# single global slot, so N closes inside one grace period left only the newest
# recoverable and orphaned the other N-1 while they were still alive and undoable.
# @undo_win/@undo_tok are kept as a mirror of the top entry: they are the legacy
# read path and what tests/pane-release.sh asserts against.
cockpit_ws_undo_push() {
  local tmux=${COCKPIT_TMUX:-"tmux -L cockpit"} win="$1" tok="$2" stack
  stack=$($tmux show -gqv @undo_ws_stack 2>/dev/null)
  $tmux set -g @undo_ws_stack "${stack:+$stack }$win:$tok"
  $tmux set -g @undo_kind ws; $tmux set -g @undo_win "$win"; $tmux set -g @undo_tok "$tok"
}

# Print "window_id<TAB>token" for the newest entry that is STILL a pending close,
# dropping it and any stale entries above it. A window that was already reaped or
# re-closed carries a different @closing token, so a stale entry can never
# resurrect it. Returns 1 when nothing is recoverable.
cockpit_ws_undo_pop() {
  local tmux=${COCKPIT_TMUX:-"tmux -L cockpit"} entry win tok
  local -a stack=()
  read -r -a stack <<<"$($tmux show -gqv @undo_ws_stack 2>/dev/null)"
  while (( ${#stack[@]} )); do
    entry="${stack[-1]}"; unset 'stack[-1]'
    win="${entry%%:*}"; tok="${entry#*:}"
    if [[ -n "$win" && -n "$tok" && "$($tmux show -wqv -t "$win" @closing 2>/dev/null)" == "$tok" ]]; then
      $tmux set -g @undo_ws_stack "${stack[*]-}"
      # re-point the legacy mirror at the new top (empty once the stack is drained)
      local top="${stack[-1]-}"
      $tmux set -g @undo_win "${top%%:*}"; $tmux set -g @undo_tok "${top#*:}"
      printf '%s\t%s' "$win" "$tok"; return 0
    fi
  done
  $tmux set -g @undo_ws_stack ""; $tmux set -g @undo_win ""; $tmux set -g @undo_tok ""
  return 1
}

# How many closes are still pending and recoverable — what Alt-u has left to give.
cockpit_ws_undo_pending() {
  local tmux=${COCKPIT_TMUX:-"tmux -L cockpit"} entry win tok n=0
  local -a stack=()
  read -r -a stack <<<"$($tmux show -gqv @undo_ws_stack 2>/dev/null)"
  for entry in "${stack[@]}"; do
    win="${entry%%:*}"; tok="${entry#*:}"
    [[ -n "$win" && -n "$tok" && "$($tmux show -wqv -t "$win" @closing 2>/dev/null)" == "$tok" ]] && n=$((n+1))
  done
  printf '%s' "$n"
}

# Resolve a target workspace WINDOW id by name: reuse if it exists, else create
# one (as a bare shell). Empty name → the workspace currently in view. Shared by
# cockpit-spawn and cockpit-send so remote/scripted placement matches the picker.
cockpit_resolve_workspace() {
  local tmux=${COCKPIT_TMUX:-"tmux -L cockpit"} name="$1" w
  [[ -n "$name" ]] || { cockpit_cur_window; return; }
  w=$($tmux list-windows -t "$COCKPIT_SESSION" -f "#{==:#{window_name},$name}" -F '#{window_id}' 2>/dev/null | head -1)
  [[ -n "$w" ]] || w=$($tmux new-window -t "$COCKPIT_SESSION" -P -F '#{window_id}' -n "$name" "bash -l" 2>/dev/null)
  echo "$w"
}

# Canonical managed-pane metadata belongs to the Cockpit launch/adoption path.
# @state is deliberately cleared here: only cockpit-poller projects it after a
# known session is available. This prevents a reused pane from carrying a prior
# session's state into a new provider process.
cockpit_clear_projection() {
  local pane="$1" tmux=${COCKPIT_TMUX:-"tmux -L cockpit"}
  $tmux set -p -t "$pane" @state ""
  $tmux set -p -t "$pane" @hook_state ""
  $tmux set -p -t "$pane" @hook_at ""
}

cockpit_stamp_known_agent() { # pane session-id cwd label agent
  local pane="$1" sid="$2" cwd="$3" label="$4" agent="$5" tmux=${COCKPIT_TMUX:-"tmux -L cockpit"}
  cockpit_clear_projection "$pane"
  $tmux set -p -t "$pane" @session_id "$sid"
  $tmux set -p -t "$pane" @cwd "$cwd"
  $tmux set -p -t "$pane" @label "$(ck_clean_label "$label")"
  $tmux set -p -t "$pane" @agent "$agent"
  $tmux set -p -t "$pane" @born ""
  $tmux set -p -t "$pane" @badge "starting"
}

cockpit_stamp_pending_agent() { # pane cwd label agent
  local pane="$1" cwd="$2" label="$3" agent="$4" tmux=${COCKPIT_TMUX:-"tmux -L cockpit"}
  cockpit_clear_projection "$pane"
  $tmux set -p -t "$pane" @session_id ""
  $tmux set -p -t "$pane" @cwd "$cwd"
  $tmux set -p -t "$pane" @label "$(ck_clean_label "$label")"
  $tmux set -p -t "$pane" @agent "$agent"
  $tmux set -p -t "$pane" @born "$(date +%s)"
  $tmux set -p -t "$pane" @badge "starting"
}

# --- seeded first-turn requests (cockpit-spawn --request-id …) ---------------
# Durable, prompt-free request records for atomically-seeded Claude launches.
# One compact mode-0600 JSON file per request under a mode-0700 state dir; the
# file name is a digest of the caller's request id (ids may carry ':' etc. and
# must never influence a filesystem path). NO prompt content is ever stored
# here — only identity, digest/byte material, binding, and status.
COCKPIT_SEED_DIR_DEFAULT="${XDG_STATE_HOME:-$HOME/.local/state}/cockpit/seeds"
# The seed state dir must be a REAL directory (never a symlink) OWNED by the current user, private (0700).
# Everything below fails closed when it is not — a symlinked or foreign-owned state dir would let a planted
# leaf redirect record/staging writes onto an external victim file.
cockpit_seed_dir() {
  local d="${COCKPIT_SEED_DIR:-$COCKPIT_SEED_DIR_DEFAULT}"
  [[ -L "$d" ]] && return 1
  mkdir -p "$d" 2>/dev/null || true
  [[ ! -L "$d" && -d "$d" && -O "$d" ]] || return 1
  chmod 700 "$d" 2>/dev/null || return 1
  printf '%s' "$d"
}
cockpit_seed_record_path() {  # $1 = request id — fails when the state dir is unavailable/unsafe
  local d; d=$(cockpit_seed_dir) || return 1
  printf '%s/%s.json' "$d" "$(printf '%s' "$1" | sha256sum | cut -c1-40)"
}
cockpit_seed_staging_path() { # $1 = request id — producer-owned protected prompt staging (unlinked at launch)
  local d; d=$(cockpit_seed_dir) || return 1
  printf '%s/%s.prompt' "$d" "$(printf '%s' "$1" | sha256sum | cut -c1-40)"
}
# Every leaf write goes through a private mktemp (O_EXCL, 0600, unpredictable name) followed by rename():
# rename replaces a (possibly attacker-planted) leaf ITSELF and never follows it, so no write can ever be
# redirected through a symlink. Leaves are additionally refused outright when symlinked.
cockpit_seed_write() {        # $1 = path, $2 = one-line json — atomic, symlink-safe, 0600
  local path="$1" json="$2" tmp
  [[ -L "$path" ]] && return 1
  tmp=$(umask 077; mktemp "$(dirname "$path")/.tmp.XXXXXXXX") || return 1
  printf '%s\n' "$json" > "$tmp" || { rm -f "$tmp"; return 1; }
  mv -f "$tmp" "$path"
}
# Claim a lock file safely: refuse a symlink leaf; create-if-absent with noclobber (O_EXCL — refuses to
# follow even a dangling planted symlink); verify a plain regular file remains.
cockpit_seed_locksafe() {     # $1 = lock path
  local lock="$1"
  [[ -L "$lock" ]] && return 1
  if [[ ! -e "$lock" ]]; then ( set -C; umask 077; : > "$lock" ) 2>/dev/null || true; fi
  [[ -f "$lock" && ! -L "$lock" ]]
}
# flock'd read-modify-write: apply a jq filter (with --arg pairs) to the record
# and persist atomically; echoes the new JSON. Serializes spawn / in-pane
# launcher / hook writers so a status transition can never be torn or doubled.
cockpit_seed_update() {       # $1 = path, $2 = jq filter, rest = jq args
  local path="$1" filter="$2"; shift 2
  [[ -L "$path" ]] && return 1
  cockpit_seed_locksafe "$path.lock" || return 1
  (
    flock -x 9 || exit 1
    local cur new
    [[ -L "$path" ]] && exit 1
    cur=$(cat "$path" 2>/dev/null) || exit 1
    new=$(printf '%s' "$cur" | jq -c "$filter" "$@") || exit 1
    cockpit_seed_write "$path" "$new" || exit 1
    printf '%s' "$new"
  ) 9<"$path.lock"
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
  # Read with \x1f, not tab: tab is IFS-whitespace, so an empty title field would
  # collapse and shift cwd into title. And flatten newlines/tabs out of the title
  # IN SQL — read is line-based, so a title with an embedded newline used to split
  # the row and leave an empty id → `ST[$id]: bad array subscript` crash.
  while IFS=$'\x1f' read -r id status title cwd; do
    [[ -n "$id" ]] || continue
    ST[$id]="$status"; TL[$id]="$title"; CW[$id]="$cwd"
  done < <(sqlite3 -separator $'\x1f' "$SANTA_DB" \
    "SELECT id, status, replace(replace(replace(coalesce(nullif(summary_title,''), substr(first_user_text,1,80), ''),char(10),' '),char(13),' '),char(9),' '), coalesce(cwd,'') FROM sessions;")
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
  # Read with \x1f, not tab: tab is IFS-whitespace, so an empty title field would
  # collapse and shift cwd into title. And flatten newlines/tabs out of the title
  # IN SQL — read is line-based, so a title with an embedded newline used to split
  # the row and leave an empty id → `ST[$id]: bad array subscript` crash.
  while IFS=$'\x1f' read -r id status title cwd; do
    [[ -n "$id" ]] || continue
    ST[$id]="$status"; TL[$id]="$title"; CW[$id]="$cwd"
  done < <(sqlite3 -separator $'\x1f' "$SANTA_DB" \
    "SELECT id, status, replace(replace(replace(coalesce(nullif(summary_title,''), substr(first_user_text,1,80), ''),char(10),' '),char(13),' '),char(9),' '), coalesce(cwd,'') FROM sessions;")
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

# The directory a claude session must be resumed FROM. `claude --resume <id>`
# resolves the id inside the project dir derived from its CWD, so resuming from
# anywhere else fails outright ("No conversation found with session ID") and the
# pane dies on the spot. A pane's recorded @cwd is NOT reliable for this: the
# Claude hook restamps it from the session's LIVE cwd (see cockpit-hook), so a
# session that started in ~/work and cd'd into ~/work/sub ends up recorded under
# the subdirectory while its transcript stays in the original project dir. That
# drift is invisible until a restore, which then silently deletes the workspace.
#
# So: keep the recorded cwd when it genuinely owns the transcript, else recover
# the launch cwd from the transcript itself (its first entry records it) and use
# that — but only when it encodes back to the directory the transcript actually
# lives in, so a malformed transcript can never redirect us somewhere arbitrary.
claude_launch_cwd() {   # id cwd → a cwd that can resume this session
  local id="$1" cwd="$2" jsonl first
  [[ -f "$PROJECTS_DIR/$(encode_project_dir "$cwd")/$id.jsonl" ]] && { printf '%s' "$cwd"; return; }
  jsonl=$(session_jsonl "$id" "$cwd")
  [[ -n "$jsonl" && -f "$jsonl" ]] || { printf '%s' "$cwd"; return; }
  first=$(grep -m1 -o '"cwd":"[^"]*"' "$jsonl" 2>/dev/null | head -1 | cut -d'"' -f4)
  if [[ -n "$first" && -d "$first" \
        && "$(encode_project_dir "$first")" == "$(basename "$(dirname "$jsonl")")" ]]; then
    printf '%s' "$first"; return
  fi
  printf '%s' "$cwd"
}

# Inner shell command to (re)launch a pane for (agent, id, cwd[, rc-name]).
# claude panes come up with `--remote-control` (see cockpit_rc_args); the optional
# 4th arg is the name shown in the Claude app — pass the session's title where you
# have it, else it falls back to the cwd basename. Codex is unaffected: `codex
# resume` resolves a rollout by uuid across the whole store, so its cwd is just
# where the work happens and carries no resolution meaning.
agent_resume_inner() {
  local agent="$1" id="$2" cwd="$3" name="${4:-}"
  case "$agent" in
    codex) printf 'cd %q && exec codex%s resume %s' "$cwd" "$(cockpit_codex_launch_args)" "$id";;
    *)     cwd=$(claude_launch_cwd "$id" "$cwd")
           printf 'cd %q && exec claude --resume %s%s' "$cwd" "$id" "$(cockpit_rc_args "$name" "$cwd")";;
  esac
}

# Wrap a launch command so a FAILED start leaves the pane alive instead of
# taking the workspace down with it. tmux closes a pane when its command exits,
# and closes the WINDOW when that was its last pane — so one resume that exits
# immediately silently deletes a whole workspace, and the next poller autosave
# then erases it from the layout for good. There is no undo for that: the only
# record it ever existed is the snapshot that just got overwritten.
# A clean exit still closes the pane exactly as before; only failure is caught.
cockpit_keep_pane_on_failure() {
  local inner="$1"
  inner="${inner/exec claude/claude}"; inner="${inner/exec codex/codex}"
  printf '%s; rc=$?; [ "$rc" = 0 ] || { printf "\\n\\033[31mcockpit: launch failed (exit %%s)\\033[0m — keeping this pane so the workspace survives.\\n" "$rc"; exec bash -l; }' "$inner"
}

# Offset queued Codex starts so a bulk restore does not make every process
# initialize the shared CODEX_HOME SQLite runtime at once.  $2 is the zero-based
# launch ordinal after the caller has put the visible workspace first.  The
# delay lives inside the pane command, so Cockpit can build and attach the whole
# grid immediately instead of blocking the restore loop between launches.
cockpit_stagger_agent_command() {
  local agent="$1" ordinal="$2" inner="$3"
  local interval="${COCKPIT_CODEX_STAGGER_SECS:-3}" delay notice
  [[ "$agent" == codex ]] || { printf '%s' "$inner"; return; }
  [[ "$interval" =~ ^[0-9]+$ ]] || interval=3
  [[ "$ordinal" =~ ^[0-9]+$ ]] || ordinal=0
  delay=$(( ordinal * interval ))
  (( delay > 0 )) || { printf '%s' "$inner"; return; }
  notice="cockpit: Codex startup queued for ${delay}s (shared-state stagger)"
  printf 'printf "%%s\\n" %q; sleep %d; %s' "$notice" "$delay" "$inner"
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
