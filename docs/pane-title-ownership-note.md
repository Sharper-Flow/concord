# Note: pane/tab title ownership — lessons from zlauncher + Advance

**Status:** Informational note, not a decision record. No requirements.
**Date:** 2026-08-12
**Source:** `addZlauncherPaneTitles` (toolbox, shipped 2026-08-12). Proposal and design artifacts in toolbox ADV archive.

Concord's launcher (CD-0014, Bubble Tea TUI) will eventually own the pane/tab title
surface currently split between zlauncher and the Advance plugin. These are
observations from building that surface, recorded so the Concord launcher
implementation can make its own informed choices.

## What exists today

Two writers under different ownership:

| Surface | Writer | Mechanism |
|---|---|---|
| Tab title | zlauncher | `zellij action rename-tab-by-id` (fail-closed, verified) |
| Pane title (no active change) | zlauncher | `zellij action rename-pane -p` (fail-soft, added by `addZlauncherPaneTitles`) |
| Pane title (active change) | Advance plugin | OSC 0 → `/dev/tty`, overwrites the launcher title within seconds |

The Advance plugin's `rq-titleIdentity01` [MUST] forbids project-name fallback,
which produced `Pane #1` for plain projects until zlauncher added its own pane
rename. The plugin has no opt-out for its title emission.

## Observations

1. **Two writers racing is the root problem.** The launcher sets a title once at
   exec; the plugin overwrites it live. Neither knows the other exists. The user
   sees whichever writer fired last. A single writer with an explicit precedence
   chain avoids this.

2. **Human titles need a disk source.** The launcher resolved human change titles
   from the projection `changes[].title` — one JSON read, worked well. Initiative
   titles have a bounded projection source and do not fall back to raw
   id. Any projection that feeds a title surface should carry display titles for
   both changes and Initiatives.

3. **A resident TUI solves the freeze problem.** zlauncher execs into OpenCode —
   its title is frozen at launch. The plugin's live updates are better because
   they track the active change as it shifts mid-session. Concord's Bubble Tea
   launcher stays resident, so it can own live title updates directly without
   the exec-freeze limitation.

4. **Fail-soft pane rename.** A failed pane rename must never block OpenCode
   launch. The tab rename is deliberately fail-closed (a silent `Tab #<number>`
   misroutes the user between sessions); the pane rename is deliberately fail-soft
   (a missing pane title degrades to a placeholder). This asymmetry is correct.

5. **Kill-switch convention.** `ZELLIJ_PROJECT_PANE_TITLE=0` matches the existing
   `ZELLIJ_PROJECT_TAB_IDENTITY=0` / `ZELLIJ_PROJECT_COLOR=0` pattern. Concord
   should define its own equivalent.

6. **Font size is impossible.** Verified at every layer: zellij 0.44.3 has no
   font key in `config.kdl`, no `--font-size` CLI option, and a colours-only theme
   struct; the plugin API renders through SGR and no font-size SGR exists in
   ECMA-48; Windows Terminal font size is profile-level only. Prominence must
   come from colour/weight (theme keys), not size.
