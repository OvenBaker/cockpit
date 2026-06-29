# cockpit

A tmux control surface for your most-recent unfinished Claude Code sessions —
resume several at once into a titled, colour-coded grid and steer them from one
keyboard. Runs on a dedicated tmux socket (`tmux -L cockpit`) so it never
touches your other tmux use.

## Usage

```
cockpit              restore the saved layout if one exists, else pick fresh
cockpit --fresh      ignore the saved layout; pick sessions fresh
cockpit --restore    force-restore the saved layout
cockpit --santa      pick sessions in santa's TUI (resume → cockpit)
cockpit --rebuild    tear down a running cockpit, then build a fresh one
cockpit --auto       skip the picker: just open the top -n sessions
cockpit --list       dry run: show the candidate sessions
cockpit -n N         default selection / --auto pane count (default 5)
cockpit --attach     just attach to an existing cockpit
cockpit --kill       tear down the cockpit server
```

## Persistence

The poller continuously saves the layout (workspaces, panes, and which session
each holds) to `~/.local/state/cockpit/layout.<session>.tsv` — throttled and
only on change, so an ungraceful shutdown loses at most a few seconds. A plain
`cockpit` then rebuilds that layout and resumes every session. `--kill` keeps
the saved layout; `--fresh` ignores it.

## Agents (Claude + Codex)

cockpit tracks both **Claude Code** and **Codex** sessions side by side. Each
pane carries an `@agent` stamp; classification, the resume command, and session
discovery dispatch per provider:

| | Claude | Codex |
|---|---|---|
| transcripts | `~/.claude/projects/**/<id>.jsonl` | `~/.codex/sessions/**/rollout-*-<id>.jsonl` |
| done signal | `end_turn` | `event_msg/task_complete` |
| resume | `claude --resume <id>` | `codex resume <id>` |

The picker, candidates, and restore merge both by recency (tagged `cl`/`cx`);
`Alt-N` asks which agent to start. Cross-agent search/`related` in santa is a
follow-on (it indexes Claude transcripts today).

## Live state

Each pane's **top title** is coloured by the session's live state, read from its
transcript: **green** working · **blue** just-finished · **red** needs-input ·
**dim** idle. Box borders are neutral; the active pane's border is **yellow**.
The label hugs the left; status + time hug the right.

## Keys (no prefix)

| Key | Action |
|-----|--------|
| `Alt-1`…`9` | jump to pane N |
| `Alt-Tab` | next attention-worthy pane (needs-input > just-finished > working) |
| `Alt-z` | zoom / unzoom the active pane |
| `Alt-i` | collapse idle panes / restore |
| `Alt-r` | retarget pane → pick a dormant session |
| `Alt-n` | add a pane → pick a session |
| `Alt-s` | browse santa's TUI; resume there sends the session here |
| `Alt-x` | remove the active pane |
| `Alt-/` (or `Alt-h` / `Alt-?`) | key reference popup |

## Pieces

- `cockpit` — launcher (picker, grid build, keybinds/chrome).
- `lib.sh` — session selection + JSONL state classification.
- `cockpit-poller` — background daemon (singleton) painting live state onto borders.
- `cockpit-pick` — numbered chooser (startup multi-select / retarget / add).
- `cockpit-send` — resume a given session as a pane (or queue if no grid).
- `shim/wt.exe` — stand-in so santa's resume can target cockpit.
- `cockpit-next`, `cockpit-toggle-idle`, `cockpit-pane`, `cockpit-help`.

## Dependencies

`tmux` (≥3.4), `bash`, `jq`, `sqlite3`, and
[santa](https://github.com/OvenBaker/santa) for session metadata and the
`--santa` picker. Designed for WSL + Windows Terminal.

---

Part of a trio: **[santa](https://github.com/OvenBaker/santa)** (search & resume your
Claude + Codex history) and **[agent-fusion](https://github.com/OvenBaker/agent-fusion)**
(run Claude + Codex on one task and fuse the output). See
**[agent-tooling](https://github.com/OvenBaker/agent-tooling)** for how they fit together.
