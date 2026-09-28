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
| resume | `claude --resume <id>` | `codex --remote ws://127.0.0.1:43129 resume <id>` |

The picker, candidates, and restore merge both by recency (tagged `cl`/`cx`);
`Alt-N` (the new-agent picker, mapped to Ctrl-Shift-N in the Windows Terminal setup) asks which agent to start.
On its agent step, **Ctrl-B** toggles **Bypass permissions for this start only**; the checkbox starts off
every time. Claude normal starts explicitly use auto permissions. Codex bypass starts are standalone
because the shared remote TUI cannot accept per-start permission overrides; ordinary Codex starts
continue to use the shared backend. This choice is not saved as a pane or workspace default.

The directory list starts with the originating pane's current Git repository (or its current directory
outside Git), then its current subdirectory and recent paths without duplicates. **Ctrl-N** opens the
new-path prompt; **Tab** completes filesystem paths, including spaces, and **Ctrl-U** clears the line.
Invalid directories can be corrected without losing the agent choice. `/` still filters recent paths.
On the workspace step, **Ctrl-N** jumps straight to naming a new workspace. Both shortcuts work while
filtering, even when the action row is hidden. Cancelling leaves panes and workspaces untouched. Cross-agent search/`related` in santa is a
follow-on (it indexes Claude transcripts today).

Bulk starts queue Codex panes three seconds apart so they do not all initialize
the shared `CODEX_HOME` SQLite state simultaneously. During restore, Codex panes
in the saved active workspace start first; workspace and pane order do not
change. Override the interval with `COCKPIT_CODEX_STAGGER_SECS` (`0` disables it).

### Codex mobile control through Windows and WSL

Codex panes attach to a shared WSL app-server. The Windows ChatGPT app connects
to that same server and provides the paired phone connection. The primary store
is `~/.codex` inside Linux; there is no SSH connection or history synchronization.

Install the backend from the live deployment:

```bash
install -m 644 ~/tools/cockpit/systemd/cockpit-codex-server.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now cockpit-codex-server.service
```

The server binds only `127.0.0.1:43129`. Its script loads the user's Node runtime,
uses `~/.local/bin/codex`, and keeps both configuration and databases in the
Linux home. Enable user lingering if the service should survive logout.
The service supplies cockpit's `danger-full-access`, `on-request` and
`auto_review` defaults. Remote TUI resumes reject permission flags on the client;
standalone launches still pass those flags directly. Saved thread settings and
explicit desktop choices can affect a resumed thread's effective permissions.

From WSL, install the local Windows launcher and Start-menu/sign-in shortcuts.
Resolve the deployment symlink before crossing the Windows filesystem boundary:

```bash
powershell.exe -NoProfile -ExecutionPolicy Bypass -File \
  "$(wslpath -w "$(readlink -f ~/tools/cockpit/windows/Install-CodexLauncher.ps1)")"
```

Then launch it from Windows PowerShell:

```powershell
& "$env:LOCALAPPDATA\Cockpit\Launch-Codex.ps1" -Restart
```

Use **ChatGPT (Cockpit)** from the Start menu. This launcher starts the WSL service,
waits for readiness, and sets `CODEX_APP_SERVER_WS_URL` only for the app process.
If the app is already running it leaves it alone unless `-Restart` is supplied.
The app's ordinary shortcut does not apply the connection override. Keep the
app's own independent sign-in startup disabled to avoid a competing launch.
The installer accepts `-Distribution` and `-LinuxUser` for other WSL setups.

Keep these preferences in the primary Linux config so the Windows UI does not
mistakenly request native Windows sandbox setup:

```toml
[desktop]
runCodexInWindowsSubsystemForLinux = true
integratedTerminalShell = "wsl"
```

Ordinary new, seeded and resumed Codex panes use the shared endpoint and pass
their cwd explicitly. Orbital sessions bound to an `orb` server (including
brief-studio and execution profiles) stay standalone with a diagnostic: the
remote TUI does not preserve their per-invocation MCP/developer configuration.
They are not mobile-enabled by this integration. Existing standalone
panes are migrated individually with `cockpit-restart <pane>` when idle. A backend
readiness failure refuses a pane restart before killing its current process.
Restarting a pane only replaces its client; `systemctl --user restart
cockpit-codex-server` restarts the backend for **all** attached sessions and should
be scheduled while they are idle. After a backend restart, clients may need to
resume their sessions again. Update the Windows launcher copy by rerunning its installer
after a cockpit deployment that changes `windows/Launch-Codex.ps1`.

For rollback, use `COCKPIT_CODEX_REMOTE=0 cockpit-restart <pane>` or pass the same
environment setting to a new launch. `COCKPIT_CODEX_REMOTE_URL` overrides the
WebSocket address for clients; coordinate any address change with the backend
and Windows launcher. There is no silent fallback to standalone mode.

The Windows app endpoint override is an implementation-level integration tested
with app 26.903.9818.0 and CLI 0.154.0, not a documented desktop setting. Recheck
desktop/mobile/terminal operation after app updates.

## Claude accounts (per pane)

Run some panes under a **second Claude subscription** while the rest stay on the
primary one. This is auth only: transcripts, santa's index, the poller and the
hooks all keep using `~/.claude`, because the binding is a token handed to the
pane's child process — not a relocated config directory.

**Mint the token.** Log in to the CLI as the account you want, then:

```
claude setup-token                    # prints a long-lived token
install -m 600 /dev/null ~/.config/cockpit/accounts/work2.token
printf '%s' '<the token>' > ~/.config/cockpit/accounts/work2.token
```

**Choosing an account when you spawn (Alt-N).** The type prompt lists every account that
resolves, on numeric keys — `1` (or Enter) is always the default logged-in account and `2`
onward are your configured ones, so a key means the same thing tomorrow as today:

```
type?   [Enter/1 = claude (default) · 2 = claude (work2) · c = codex · b = bash shell]
```

A token file that is missing, empty or mis-chmodded is named as unavailable rather than
silently omitted — an offered key that then refuses is worse than no key.

**The file convention.** `${COCKPIT_ACCOUNTS_DIR:-~/.config/cockpit/accounts}/<name>.token`
holds the token alone. It is a credential, so cockpit refuses it unless it is a
real regular file (not a symlink), readable by you, mode **0600** (any group or
other bit at all refuses), and non-empty after trimming whitespace. The account
`<name>` must match `^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$` — it becomes a filename
and a tmux option value.

**Use it.**

```
cockpit-spawn --cwd ~/repos/thing --account work2
cockpit-send  <session-id>        --account work2
```

Claude only. `--agent codex` and shell panes **refuse** the flag rather than
ignoring it — Codex has no equivalent token, and a pane you *think* is on the
second account but isn't is the exact thing this feature exists to prevent.

The pane carries an `@account` stamp with the NAME, which is persisted with the
layout, re-applied on restore, and reported by `cockpit-state` (`panes[].account`)
so the web surface and Orbital's fleet view can show it. **Only the name is ever
persisted.** The token reaches the pane through tmux's `-e` start-environment at
creation, so it is absent from the `bash -lc` command string, the pane's start
command, the layout DB, `cockpit-state` and the logs.

**Two verified facts about Claude Code (2.1.246) that shape all of the above:**

1. `CLAUDE_CODE_OAUTH_TOKEN` **takes precedence** over `~/.claude/.credentials.json`,
   per process — an invalid value fails loudly with `401 OAuth access token is invalid`.
   That is what makes per-pane accounts work at all.
2. `CLAUDE_CODE_OAUTH_TOKEN=""` **is treated as unset**: the run silently succeeds
   on the *primary* account. That is why cockpit refuses an empty or missing token
   instead of passing it through — passing it through is indistinguishable from
   having no binding, and burns the exact quota the binding exists to protect
   with nothing on screen to say so.

Same rule at restore: if a pane's `@account` no longer resolves to a valid token
file, the pane comes up **refusing**, in red, naming the account and the reason,
and stays alive so you can read it. It is never quietly restarted on the default
account.

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
| right-click → *Half width / Full width* | flip the solo fit for a one-pane workspace (`COCKPIT_SOLO_WIDTH`, default 50; 0 disables) |
| `Alt-i` | collapse idle panes / restore |
| `Alt-r` | retarget pane → pick a dormant session |
| `Alt-n` | add a pane → pick a session |
| `Alt-s` | browse santa's TUI; resume sends the session here, or jumps to it if already open |
| `Alt-x` | remove the active pane |
| `Alt-/` (or `Alt-h` / `Alt-?`) | key reference popup |

## Pieces

- `cockpit` — launcher (picker, grid build, keybinds/chrome).
- `lib.sh` — session selection + JSONL state classification.
- `cockpit-poller` — background daemon (singleton) painting live state onto borders.
- `cockpit-pick` — numbered chooser (startup multi-select / retarget / add).
- `cockpit-select` — shared list picker (Go) used by the popups: arrow keys, `/` to filter,
  Enter to commit, Esc to cancel. Reads TSV on stdin, prints the chosen row to stdout, and
  touches nothing else — callers keep the single tmux mutation.
- `cockpit-send` — resume a given session as a pane (or queue if no grid).
- `shim/wt.exe` — stand-in so santa's resume can target cockpit.
- `cockpit-next`, `cockpit-toggle-idle`, `cockpit-pane`, `cockpit-help`.

## Dependencies

`tmux` (≥3.4), `bash`, `jq`, `sqlite3`, and
[santa](https://github.com/OvenBaker/santa) for session metadata and the
`--santa` picker. Designed for WSL + Windows Terminal.

The Go binaries build from this repo with the standard toolchain and no third-party
dependencies beyond `golang.org/x/sys`:

```
go build -o ~/.local/bin/cockpit-core   ./cmd/cockpit-core
go build -o ~/.local/bin/cockpit-select ./cmd/cockpit-select
```

`cockpit-select` is resolved beside the scripts first (a pinned deploy dir), then from `PATH`;
`COCKPIT_SELECT` overrides both.

---

Part of a trio: **[santa](https://github.com/OvenBaker/santa)** (search & resume your
Claude + Codex history) and **[agent-fusion](https://github.com/OvenBaker/agent-fusion)**
(run Claude + Codex on one task and fuse the output). See
**[agent-tooling](https://github.com/OvenBaker/agent-tooling)** for how they fit together.

## License

[The Unlicense](LICENSE) — released into the public domain. Do whatever you want.


### Isolated Claude accounts

`claude-profile` resolves account state for Cockpit and `bclaude`. Register an absolute
config directory in `~/.config/cockpit/accounts/<name>.configdir`. Keep the existing
`<name>.mark` for display. The default Claude directory remains mixed legacy history;
it does not prove the historical billing account.

For Beta (`work2`), prepare `~/.claude-beta`, then run:

```sh
claude-profile --account work2 sync
env -u CLAUDE_CODE_OAUTH_TOKEN -u ANTHROPIC_API_KEY -u ANTHROPIC_AUTH_TOKEN CLAUDE_CONFIG_DIR="$HOME/.claude-beta" claude auth login
```

`sync` uses rsync to copy plugin contents safely, including read-only Git objects. It copies common settings and plugin contents/installation records, links shared
skills, agents, commands, hooks and rules, and links existing project memory directories.
It copies personal MCP server definitions and onboarding preferences selectively from app state, while preserving Beta identity and trust. It never copies credentials or transcripts; MCP OAuth logins stay account-local. Run it deliberately after common
settings/plugin changes; Beta-local settings edits are replaced. New project memory
links are added on the next sync. Avoid running sync during plugin install/update.

Stage the registry as `<name>.configdir.pending` until login is complete; rename to
`.configdir` to switch new launches. Prefer `claude-profile --account work2 activate`: it verifies a full-scope login on a different account before promoting the pending registry. Until activated, legacy token launching continues.
A registered directory without a login refuses new launches.

Existing sessions in `~/.claude/projects` resume there using the legacy account token.
Do not move live transcripts. Resume an isolated session on its owning account; crossing
accounts is refused until a deliberate migration transfers its associated state. Cockpit
and Santa discover both roots, while Stele labels only the isolated root by its account.
`bclaude --resume <session>` follows this same rule; new native children inherit the root.

Remote Control requires a full login; inference-only legacy tokens disable Remote Control.
The legacy token files remain needed for old sessions and any quota reader using them.
No running agent needs restarting just to deploy the readers.

Checks: `python3 tests/claude-profiles.py`, `bash tests/profile-launch.sh`, and the existing
account, seeded-spawn, new-session and restore-cwd tests (all use isolated fixtures).
