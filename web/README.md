# cockpit web — control surface for your phone

A tiny web UI to drive cockpit when you're away from the desk. Two jobs:

1. **Open a new session in a named (or new) workspace with Remote Control on** —
   it appears in the Claude app, and you type the first instructions there.
2. **Reboot idle sessions that have lost Remote Control** — one tap re-launches a
   pane onto `claude --resume <id> --remote-control`; the conversation reloads
   from disk (nothing lost) and reconnects to the app.

## Pieces

| file | role |
|---|---|
| `cockpit-webd` | single-file Node server (no deps), binds `127.0.0.1:8838` |
| `web/index.html` | the mobile page (self-contained, dark/light) |
| `cockpit-state` | emits the live grid as JSON (`/api/state`) |
| `cockpit-spawn` | non-interactive "new session" (`/api/new`) |
| `cockpit-reboot` | relaunch a pane with Remote Control (`/api/reboot`) |

Remote Control itself is now structural: **every claude pane launches with
`--remote-control`** (see `cockpit_rc_args` in `lib.sh`). Set
`COCKPIT_REMOTE_CONTROL=0` to go back to plain panes. This is the actual fix for
"resumes without RC" — the flag is explicit on every spawn/resume/restore, not
left to the `remoteControlAtStartup` setting.

## Run it locally

```
node cockpit-webd            # http://127.0.0.1:8838
# or as a service — first EDIT COCKPIT_WEB_ALLOW_EMAIL in the unit to your Access
# email(s) (it ships with a you@example.com placeholder):
cp web/cockpit-webd.service ~/.config/systemd/user/
systemctl --user daemon-reload && systemctl --user enable --now cockpit-webd
loginctl enable-linger "$USER"   # survive logout / start at boot
```

Test on the LAN first: `curl -s localhost:8838/api/state | jq .running`.

## Expose it: Cloudflare Tunnel + Access → cockpit.<domain>

The server binds loopback only; a tunnel is how the phone reaches it, and Access
is the auth gate.

A **dedicated** user-service tunnel is used here, alongside (never touching) any
existing `cloudflared.service`.

```bash
# 1. one-time auth (interactive browser — pick your zone):
cloudflared tunnel login

# 2. create the tunnel + DNS route:
cloudflared tunnel create cockpit
cloudflared tunnel route dns cockpit cockpit.<domain>

# 3. config: ~/.cloudflared/config.yml  (from cloudflared-config.yml.example,
#    fill <TUNNEL_UUID>; ingress cockpit.<domain> -> http://127.0.0.1:8838)

# 4. run it as a user service (coexists with the existing system tunnel):
cp web/cloudflared-cockpit.service ~/.config/systemd/user/
systemctl --user daemon-reload && systemctl --user enable --now cloudflared-cockpit
```

Then create the Access self-hosted app + allow-policy — scripted (needs a token
with **Access: Apps Edit** on the account):

```bash
CF_ACCESS_TOKEN=<token> CF_ACCOUNT=<cf account id> \
  APP_DOMAIN=cockpit.<domain> ALLOW_EMAIL=you@example.com[,other@you.com] \
  web/setup-access.sh
```

…or by hand: Zero Trust → Access → Applications → Add self-hosted app for
`cockpit.<domain>`, policy Allow → Emails include your address(es).

`COCKPIT_WEB_ALLOW_EMAIL` (in the webd unit) must list the SAME emails: Access
injects `Cf-Access-Authenticated-User-Email` and the app re-checks it, so a
mismatch means Access lets you in and the app then 403s you.

**Verify before trusting it:** hit `https://cockpit.<domain>` with no Access
session — you must get the Access login, and a request with a forged email header
must still be bounced. Only then is it safe to use.

### Alternative: reuse an existing tunnel

If you already run a tunnel whose account holds your zone, add a public hostname
to it (Zero Trust → Networks → Tunnels → your tunnel → **Public Hostname → Add**
→ `cockpit` / your domain / `HTTP` `127.0.0.1:8838`) instead of the CLI steps,
then the same Access app. No new local service.

## Notes / edges

- **Folder trust:** a brand-new cwd makes `claude` pause on "trust this folder?".
  The recent-cwd list is already-trusted project dirs, so this only bites truly
  new paths — approve at the desk, or pre-trust the dir.
- **Codex** panes have no Remote Control, so the UI shows them read-only.
- **Orderly** panes (brief/alpha/bravo) are the aide fleet — shown dimmed, never
  rebooted from here (that's `orderlies-up`'s job).
- Reboot skips **working / needs-input** panes unless forced, so you never
  interrupt a turn in flight.
