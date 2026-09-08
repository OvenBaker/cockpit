# cockpit — agent guidelines

**`~/repos/cockpit` is the DEVELOPMENT root. It is never the live tool.**

The live cockpit is `~/tools/cockpit → ~/workspaces/lean/.deploy/cockpit`: a detached worktree of this repo
pinned to a `main` commit. That is a STABLE path — nothing points at a sha, so nothing goes stale when a
deploy moves it (the earlier `cockpit-<sha>` directories were abandoned for exactly that reason).

- **Never point a service, config, or agent at `~/repos/cockpit`.** Resolve cockpit tools via
  `~/tools/cockpit` only (Orbital: `ORBITAL_COCKPIT_DIR`). Incident 2026-07-23: Orbital's `COCKPIT_DIR`
  defaulted to this checkout, which was three weeks stale — its `cockpit-spawn` predated the seeded-launch
  contract and every seeded brief-workshop launch failed with `unknown arg: --request-id`. The failure
  looked environment-shaped and cost a long diagnosis.
- **Scripts run by hand from a worktree act on your live tmux.** Fine for a quick check; anything that
  sets hooks or binds is tested on a scratch server first (`tmux -L <name>` with `COCKPIT_TMUX` and
  `COCKPIT_SESSION` exported, as `cockpit-ws fit` was).
- **Keep it scrubbed.** No build outputs or generated files here (`git clean -ndx` stays empty).
- `cockpit-core` and `cockpit-select` are separately compiled binaries in `~/.local/bin` and are not part
  of a cockpit deploy.

## Delivery workflow

These are personal tools. A mistake costs a `git revert` and a redeploy; unlanded work costs far more,
because it blocks the next change and rots. Rules, each with the failure it prevents:

- **The root checkout (`~/repos/cockpit`) stays on `main`, clean, and is never edited directly.** Every change
  starts in its own worktree under `~/workspaces/lean/.worktrees/`:
  `git -C ~/repos/cockpit worktree add ~/workspaces/lean/.worktrees/cockpit-<slug> -b <type>/<slug> main`
  *Prevents: adjacent uncommitted work in the root blocking an unrelated deploy (2026-09-08, cockpit: a
  finished feature and a half-done change shared files in the root, so neither could ship).*
- **One worktree, one change.** Something unrelated you notice gets its own worktree, not a hunk here.
- **Land the moment it works.** No PR gate for solo work: rebase onto `main`, run the checks the change
  touches, then `git -C ~/repos/cockpit merge --ff-only <branch>` and push (if the repo has a remote).
  *Prevents: branches ageing into conflicts (orbital carried 15+ unlanded branches on 2026-09-08).*
- **Deploy on every landing** (docs-only landings excepted). `main` is what runs — see Deploy below.
  *Prevents: live drifting from `main`, so the next deploy silently rolls a change forward or back
  (2026-09-08: orbital ran two commits `main` did not have; cockpit's dev root was 91 commits behind).*
- **Never deploy a commit that is not on `main`.** Rollback is a deploy of an earlier `main` commit.
- **Clean up right after landing:** `git worktree remove <path> && git branch -d <branch>`. Weekly sweep:
  `git worktree prune && git branch --merged main | grep -v ' main$' | xargs -r git branch -d`, then
  `git worktree list` — anything older than a week is landed or deleted, and superseded deploy dirs go.
- **WIP never lives in the root.** A paused change is committed as `wip:` on its own branch in its
  worktree — not stashed, not left dirty.

### Deploy

Shell scripts plus `web/` — no build step. Three lines, then verify:

```bash
git -C ~/workspaces/lean/.deploy/cockpit checkout --detach main   # the live worktree tracks a main commit
systemctl --user restart cockpit-webd                             # web daemon runs from ~/tools/cockpit
~/tools/cockpit/cockpit --reload                                  # binds/menus/hooks + poller, on the running grid
git -C ~/workspaces/lean/.deploy/cockpit log --oneline -1; systemctl --user is-active cockpit-webd
```

Rollback is the same three lines with `<previous-sha>` in place of `main`. The wider deploy story (Orbital,
verification probes, why paths are stable) is in `~/workspaces/lean/orbital/docs/operations/deploy.md`.
