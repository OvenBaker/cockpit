#!/usr/bin/env bash
# setup-access.sh — create (idempotently) the Cloudflare Access self-hosted app
# that gates your cockpit hostname, allowing only the listed emails.
#
#   CF_ACCESS_TOKEN=<token: 'Access: Apps Edit'> CF_ACCOUNT=<cf account id> \
#   APP_DOMAIN=cockpit.you.com ALLOW_EMAIL=you@example.com[,other@you.com] \
#     ~/repos/cockpit/web/setup-access.sh
#
# Required: CF_ACCESS_TOKEN, CF_ACCOUNT, ALLOW_EMAIL.
# Optional: APP_DOMAIN (default cockpit.example.com), APP_NAME, SESSION_DURATION.
set -uo pipefail
ACCT="${CF_ACCOUNT:?set CF_ACCOUNT to your Cloudflare account id}"
DOMAIN="${APP_DOMAIN:-cockpit.example.com}"
NAME="${APP_NAME:-cockpit}"
# every identity you may log in as (comma-separated). MUST mirror the webd's
# COCKPIT_WEB_ALLOW_EMAIL, or Access lets you in and the app then 403s you.
EMAILS="${ALLOW_EMAIL:?set ALLOW_EMAIL to a comma-separated allow-list}"
DUR="${SESSION_DURATION:-24h}"
TOK="${CF_ACCESS_TOKEN:-}"
[[ -n "$TOK" ]] || { echo "set CF_ACCESS_TOKEN (needs 'Access: Apps Edit' on account $ACCT)" >&2; exit 2; }
B="https://api.cloudflare.com/client/v4/accounts/$ACCT/access"
cf() { curl -s -H "Authorization: Bearer $TOK" -H 'content-type: application/json' "$@"; }

# fail fast if the token can't even read Access on this account
probe=$(cf "$B/apps")
if [[ "$(jq -r '.success' <<<"$probe")" != "true" ]]; then
  echo "token can't reach Access on $ACCT: $(jq -c '.errors' <<<"$probe")" >&2; exit 1
fi

# reuse an existing app for this domain if present (idempotent)
apps=$(cf "$B/apps")
uid=$(jq -r --arg d "$DOMAIN" '.result[]? | select(.domain==$d) | (.uid // .id)' <<<"$apps" | head -1)
if [[ -n "$uid" ]]; then
  echo "app already exists for $DOMAIN → $uid"
else
  create=$(cf -X POST "$B/apps" --data "$(jq -n --arg n "$NAME" --arg d "$DOMAIN" --arg s "$DUR" \
    '{name:$n, domain:$d, type:"self_hosted", session_duration:$s, auto_redirect_to_identity:false}')")
  uid=$(jq -r '.result.uid // .result.id // empty' <<<"$create")
  [[ -n "$uid" ]] || { echo "app create failed: $(jq -c '.errors' <<<"$create")" >&2; exit 1; }
  echo "created app $NAME ($DOMAIN) → $uid"
fi

# ensure an allow-policy including EXACTLY the desired email set (create or sync)
inc=$(jq -cn --arg e "$EMAILS" '[$e|split(",")[]|gsub("^\\s+|\\s+$";"")|select(length>0)|{email:{email:.}}]')
pols=$(cf "$B/apps/$uid/policies")
pid=$(jq -r '.result[]? | select(.decision=="allow") | (.id // .uid)' <<<"$pols" | head -1)
if [[ -n "$pid" ]]; then
  pol=$(cf -X PUT "$B/apps/$uid/policies/$pid" --data "$(jq -n --argjson inc "$inc" '{name:"gareth", decision:"allow", include:$inc}')")
  action="synced"
else
  pol=$(cf -X POST "$B/apps/$uid/policies" --data "$(jq -n --argjson inc "$inc" '{name:"gareth", decision:"allow", precedence:1, include:$inc}')")
  action="created"
fi
[[ "$(jq -r '.success' <<<"$pol")" == "true" ]] || { echo "policy $action failed: $(jq -c '.errors' <<<"$pol")" >&2; exit 1; }
echo "$action allow-policy: Emails include $(jq -r '.result.include[]?.email.email' <<<"$pol" | paste -sd, -)"

echo "---"
cf "$B/apps/$uid" | jq -c '{app:.result.name, domain:.result.domain, uid:(.result.uid // .result.id), aud:.result.aud}'
echo "Access app ready. Now start the tunnel:  systemctl --user enable --now cloudflared-cockpit"
