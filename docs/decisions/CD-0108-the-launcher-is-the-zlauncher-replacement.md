# CD-0108: the launcher is the ZLauncher replacement

- **Status:** Accepted
- **Date:** 2026-09-03
- **Scope:** terminal launcher remake, ZLauncher retirement, and the launcher
  mutation boundary
- **Approval:** The operator approved the remake scope on 2026-09-03 after
  shaping in Concord work-4e5261bca6acdd5298f5b4a3.
- **Related:** CD-0014, CD-0041, CD-0048, R1, C14, C17, C18, and issue #803
- **Replaces:** the status-only launcher scope of C18
  ([`terminal-launcher-contract.md`](../terminal-launcher-contract.md)). The
  typed spec supersession lands when the successor launcher contract
  registers during the build.
- **Preserves:** the Bubble Tea v2 rendering decision (CD-0014) and the
  store-write-free launcher boundary

## Context

C18, accepted under CD-0014 and amended by CD-0041, made the launcher a
read-only status surface. It followed the operator direction of 2026-08-09:
see status and resume work in the OpenCode terminal interface, nothing else.

The operator's daily entry tool is ZLauncher (`zellij-project-launcher`), a
5,430-line bash zellij tab picker. Its loop: scan roots plus pinned
workspaces, a fuzzy pick, a new zellij tab, an OpenCode start in that tab,
session resume, and prompt pass-through, with status probes in the preview
pane.

R1 in [`clarifications.md`](../clarifications.md) kept ZLauncher the
session bootstrap layer and made it not a candidate for Concord's primary
interface.

On 2026-09-03 the operator judged the shipped launcher too far off
expectation to iterate on. The direction: delete it and remake it as the
ZLauncher replacement.

Shaping in work-4e5261bca6acdd5298f5b4a3 fixed the shape this record holds.
The operator chose a Concord-first browse loop with launch actions,
lazygit-class pane density on arrow, Enter, and Tab keys, and launch through
Concord's session bootstrap. The operator selected the feature set from the
full ZLauncher inventory plus Concord-native candidates, then accepted three
removals, four defers, and one trim.

## Decision

### D1. The launcher is remade as the ZLauncher replacement

The remake deletes `internal/launcher` and rebuilds the launcher from zero.
The core loop: browse Products and work; launch opens a zellij tab on the
work's Concord worktree and starts the OpenCode session through Concord's
session bootstrap; resume reattaches a live session; a passed prompt becomes
that work's directive.

ZLauncher is retired when the core loop and the accepted daily extras work
on the operator's real store. Retirement is the acceptance test.

### D2. The user interface is dense panes on standard keys

The interface carries lazygit-class density: panes, everything visible at
once. Navigation uses arrow keys, Enter, and Tab. No vim chords.

Rendering stays Bubble Tea v2 per CD-0014. The remake replaces the screen
model, not the dependency.

### D3. The feature set is fixed by the 2026-09-03 selection

In, first build: scan roots for projects Concord does not know yet, pins
with pin and unpin keys, most-recently-used ordering, fuzzy filter, preview
pane, two-stage pick, resume-last, `zl` command forwarding, Vision live
status, lgrep status, tab identity badges, abbreviations, an icon option,
time-ago stamps, ANSI color control, work columns for lifecycle, priority,
and urgency, blocked-work indicators, session inventory with reattach,
Domain context per Product, an in-place refresh key, and a `--list` JSON
verb.

Removed: the launch registry, because the store already owns worktree claims
and session records; in-launcher work capture; and the knowledge peek.

Deferred: typo-tolerant matching, OpenCode database and provider health,
stack hygiene, and doctor.

### D4. The launcher stays store-write-free

The launcher reads the store and executes host processes: zellij tab
creation and the OpenCode start. It performs no durable store write. Work
capture reached through a passed prompt happens inside the session through
the typed mutation surface.

C18's read-only construction survives this record. Its status-only scope
does not.

## Consequences

- `clarifications.md` R1 records the supersession: the launcher replaces
  ZLauncher and absorbs the bootstrap role.
- `vertical-integration.md` cites this record for the launcher and interface
  direction.
- C18 cites this record in its status. The typed spec supersession completes
  when the successor launcher contract registers; that contract is authored
  during the build.
- Issue #803 owns the build and the ZLauncher retirement check.
- Documents that restate the R1 split (`design-constraints.md` §6,
  `workflows.md` §0, `feature-inventory.md` §3.10,
  `self-documentation.md` §1.1) align to this record during the build.

## Rejected alternatives

**Iterate on the current launcher.** Rejected: the operator judged the gap
too wide, and the gap was the whole shape, not one defect.

**Port ZLauncher as it stands.** Rejected: predecessor law forbids routing
Concord work through predecessor paths. The remake maps features, not code.

**An action-first picker on Concord data.** Rejected by operator choice:
browse-first with launch actions was the expected shape.

**A launch registry in the new launcher.** Rejected: it duplicates
store-owned state and would drift.

**In-launcher work capture.** Rejected: it would make the launcher a second
write authority beside the typed mutation surface.

## Verification

- The manifest registers this record as an accepted decision.
- `clarifications.md` R1, `vertical-integration.md`, and the C18 status
  note cite this record.
- Repository document, knowledge-index, and link validators pass.
