#!/usr/bin/env bash
set -euo pipefail

# Create a self-contained, reversible local MCP configuration. It never
# starts a daemon, touches tmux, edits a global client config, or removes an
# existing wrapper. The caller supplies a dedicated empty-ish directory.
usage() { echo "usage: $0 --root DIR --binary PATH --socket PATH [--remove]" >&2; exit 2; }
root= binary= socket= remove=0
while (($#)); do
  case "$1" in
    --root) root=${2-}; shift 2;; --binary) binary=${2-}; shift 2;; --socket) socket=${2-}; shift 2;; --remove) remove=1; shift;; *) usage;;
  esac
done
[[ "$root" = /* && "$binary" = /* && "$socket" = /* ]] || usage
[[ "$root$binary$socket" =~ ^[A-Za-z0-9_./:-]+$ ]] || { echo "paths must be scalar-safe" >&2; exit 2; }
[[ "$root" != / && "$root" != "$PWD" ]] || { echo "refusing broad root" >&2; exit 2; }
if ((remove)); then
  [[ -f "$root/.cockpit-mcp-local" ]] || { echo "not a local MCP setup root" >&2; exit 2; }
  rm -f "$root/.cockpit-mcp-local" "$root/clients.json" "$root/mcp.token" "$root/mcp-run" "$root/codex-mcp.toml" "$root/claude-mcp.json"
  rmdir "$root" 2>/dev/null || true
  exit 0
fi
case "$socket" in "$root"/*) ;; *) echo "socket must be inside setup root" >&2; exit 2;; esac
[[ -x "$binary" ]] || { echo "binary is not executable" >&2; exit 2; }
mkdir -p "$root"; chmod 700 "$root"; umask 077
token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
printf '%s\n' "$token" >"$root/mcp.token"; chmod 600 "$root/mcp.token"
printf '{"version":1,"clients":[{"credential":"%s","clientId":"cockpit-mcp","profile":"mcp-local","capabilities":["state:read","operations:read","events:wait","capture:sanitized","interaction:nudge","interaction:pause","interaction:compact","interaction:resume"]}]}' "$token" >"$root/clients.json"
chmod 600 "$root/clients.json"
cat >"$root/mcp-run" <<EOF
#!/usr/bin/env bash
export COCKPIT_MCP_CREDENTIAL_FILE='$root/mcp.token'
exec '$binary' mcp-stdio --socket '$socket'
EOF
chmod 700 "$root/mcp-run"
cat >"$root/codex-mcp.toml" <<EOF
[mcp_servers.cockpit]
command = "$root/mcp-run"
EOF
cat >"$root/claude-mcp.json" <<EOF
{"mcpServers":{"cockpit":{"command":"$root/mcp-run","args":[]}}}
EOF
touch "$root/.cockpit-mcp-local"; chmod 600 "$root/.cockpit-mcp-local"
echo "local setup created at $root"
echo "throwaway controller: $binary daemon --test-root $root --socket $socket --tmux-socket NAME --credentials-file $root/clients.json"
echo "owner-gated live controller: $binary daemon --live-cockpit --runtime-root $root --socket $socket --credentials-file $root/clients.json"
echo "use $root/codex-mcp.toml or $root/claude-mcp.json manually; this script installed nothing globally"
