# Canonical Cockpit pane adoption

Cockpit’s existing poller is the sole producer of a managed pane’s `@state`.
Launch and resume paths stamp `@agent`, `@session_id` when known, and `@cwd`;
new sessions instead carry `@born` until the poller adopts their matching
provider transcript. A pane without a verified session binding has no state for
MCP interaction admission.

For a legitimate already-running pane that missed its normal launch/adoption
path, the review-gated migration command is explicit and bounded:

```bash
cockpit-adopt \
  --pane %NN \
  --agent claude|codex \
  --session-id PROVIDER_SESSION_UUID \
  --cwd /absolute/provider/cwd
```

It accepts only an exact Cockpit pane, rejects conflicting existing bindings,
and requires the matching provider transcript under the supplied provider/cwd.
It then stamps the established metadata and clears old projection values; the
poller produces `@state` on its next cycle. It never infers provider or state
from a pane title, process name, or terminal content.

This command is a migration aid only. Do not run it against a live pane before
reviewer GO. Panes launched outside the Cockpit repository’s launchers remain
the responsibility of their creating workflow/worktree.
