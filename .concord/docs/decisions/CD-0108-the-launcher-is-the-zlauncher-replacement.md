# CD-0108: the launcher is the ZLauncher replacement

- **Status:** Accepted (amended 2026-09-26, 2026-10-09)
- **Date:** 2026-09-03
- **Scope:** the ZLauncher replacement, its session entry route, and the
  store-write-free boundary
- **Approval:** The operator approved the remake scope on 2026-09-03 after
  shaping in Concord work-4e5261bca6acdd5298f5b4a3. The operator approved the
  single-surface amendment on 2026-09-26 and the retirement amendment on
  2026-10-09 through the Snowball coordinator (operator-delegated).
- **Related:** CD-0014, CD-0041, CD-0048, CD-0219, R1, C14, C17, and issue #803
- **Amended by:** CD-0219 (2026-10-09) retires the interactive launcher TUI;
  the replacement role continues through the non-interactive session entry
  route and the store-write-free boundary of D4 stands
- **Preserves:** the store-write-free launcher boundary, `zl` command
  forwarding, `--resume-last`, prompt pass-through, and the session bootstrap
  launch path

## Context

The operator's daily entry tool was ZLauncher (`zellij-project-launcher`), a
bash zellij tab picker whose loop scanned roots and pinned workspaces, picked a
work, opened a tab, and started or resumed an OpenCode session with an optional
prompt. R1 in [`clarifications.md`](../clarifications.md) originally kept
ZLauncher as the session bootstrap layer; CD-0108 superseded that split by
absorbing the bootstrap role into Concord.

The remake shipped as an interactive terminal launcher. The simplify lane
(Concord (CON) issue 908) deletes that interactive surface end to end: the
browse UI, the
`concord launcher` verb, the bare-invocation TUI route, and the
`internal/launcher` tree. CD-0219 records the retirement. What remains — and
what this record now governs — is the non-interactive session entry route the
remake already carried: the operator names the work, Concord starts or resumes
the session through its session bootstrap.

## Decision

### D1. The ZLauncher replacement is the session entry route

The replacement for ZLauncher's core loop is the non-interactive entry route:
`concord zl <work>` — and the `zl` argv[0] forwarding — resolves the named
work, starts the OpenCode session for it through Concord's session bootstrap,
and passes any queued prompt as that work's directive. `--resume-last` resumes
the most recently entered work. `--project` selects a member Project for the
landing under CD-0182.

ZLauncher is retired when the entry route and the accepted daily extras work on
the operator's real store. Retirement is the acceptance test.

### D2. The entry route places nothing and derives nothing

The entry route hands session identity to the session bootstrap and derives no
workflow position. The operator provides the terminal; the route creates no
tab, pane, window, or surface, exactly as CD-0078 D1 and CD-0163 state. The
interactive rendering clauses this record once carried — single-surface lists,
pane layout, and the key-driven browse loop — retired with the TUI under
CD-0219.

### D3. The feature set is the entry verbs

The accepted entry verbs are `zl <work>` forwarding with prompt
pass-through, `--resume-last`, and `--project` (CD-0182). The interactive
feature set this record once fixed for the TUI — scan roots, pins, fuzzy
filter, two-stage pick, the browse feature list, and the `--list` JSON verb —
retired with the TUI under CD-0219.

### D4. The entry route stays store-write-free

The entry route reads the store and starts the session command. It performs no
durable store write. Work capture reached through a passed prompt happens
inside the session through the typed mutation surface.

C18's read-only construction survives this record. Its status-only scope
does not.

## Consequences

- `clarifications.md` R1 records the supersession: the Concord entry route
  replaces ZLauncher and absorbs the bootstrap role.
- `vertical-integration.md` cites this record for the interface direction.
- The C18 contract and the successor launcher contract retired with the TUI
  under CD-0219; this record and CD-0163 carry their surviving statements.
- Issue #803 owns the ZLauncher retirement check; Concord (CON) issue 908
  owns the TUI deletion that shrinks the acceptance subject to the entry
  route.
- Documents that restate the R1 split (`design-constraints.md` §6,
  `workflows.md` §0, `feature-inventory.md` §3.10,
  `self-documentation.md` §1.1) align to this record during the build.

## Rejected alternatives

**Iterate on the shipped interactive launcher.** Rejected: the operator judged
the gap too wide, and issue 908 deleted the surface rather than iterating.

**Port ZLauncher as it stands.** Rejected: predecessor law forbids routing
Concord work through predecessor paths. The replacement maps features, not code.

**A launch registry in the entry route.** Rejected: it duplicates store-owned
state and would drift.

**In-route work capture.** Rejected: it would make the entry route a second
write authority beside the typed mutation surface.

## Verification

- The manifest registers this record as an accepted decision.
- `clarifications.md` R1 and `vertical-integration.md` cite this record.
- The entry verbs are proved by the `cmd/concord` zl and session tests that
  CD-0163, CD-0176, and CD-0031 name.
- Repository document, knowledge-index, and link validators pass.
