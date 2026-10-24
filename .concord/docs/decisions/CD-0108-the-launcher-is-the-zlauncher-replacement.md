# CD-0108: The ZLauncher replacement retired with the session entry route

- **Status:** Accepted (amended 2026-09-26, 2026-10-09, 2026-10-13)
- **Date:** 2026-09-03
- **Scope:** the retirement of the ZLauncher replacement, the session entry
  route it named, and the boundaries that outlive both
- **Approval:** The operator approved the remake scope on 2026-09-03 after
  shaping in Concord work-4e5261bca6acdd5298f5b4a3. The operator approved the
  single-surface amendment on 2026-09-26 and the retirement amendment on
  2026-10-09 through the Snowball coordinator (operator-delegated). The
  coordinator approved the entry-route retirement on 2026-10-13 through the
  Snowball coordinator (operator-delegated).
- **Related:** CD-0014, CD-0222, CD-0048, CD-0078, CD-0219, R1, C14, C17, and issue #803
- **Preserves:** the host's ownership of session start and resume, the typed
  mutation surface as the only work write path, and the no-durable-write
  boundary of managed entry

## Context

The operator's daily entry tool was ZLauncher, a bash multiplexer tab picker
whose loop scanned roots and pinned workspaces, picked a work, opened a tab,
and started or resumed an OpenCode session with an optional prompt. R1 in
[`clarifications.md`](../clarifications.md) originally kept ZLauncher as the session
bootstrap layer; CD-0108 superseded that split by absorbing the bootstrap
role into Concord.

The remake shipped as an interactive terminal launcher, and CD-0219 deleted
that surface end to end. What remained was the non-interactive session entry
route: the operator named the work, and Concord started or resumed the
session through its session bootstrap. That entry route is now removed as
well. The host starts and resumes operator sessions, and a host-started
session enters managed work through `concord_work_start`. What remains of
this record is the retirement of the replacement role and the boundaries that
outlive it.

## Decision

### D1. Retired: the session entry route

The session entry route — `concord zl`, the argv[0] forwarding,
`--resume-last`, and `--project` — is removed end to end, with no
compatibility alias and no fallback. The host starts and resumes operator
sessions (CD-0078). A host-started session enters managed work through
`concord_work_start` (CD-0098).

### D2. Concord places nothing and derives no workflow position at entry

No Concord component creates, names, splits, focuses, or destroys a tab,
pane, window, or session (CD-0078 D1, CD-0163). Entry into managed work
derives workflow position from the store's canonical projection (CD-0016),
never from a terminal surface or a terminal session identifier.

### D3. Retired: the entry-verb feature set

The accepted feature set this record once fixed — `zl` forwarding with prompt
pass-through, `--resume-last`, and `--project` — is removed. No replacement
feature set exists. Removed verb names appear only in negative CLI
assertions, never in published help or runtime dispatch.

### D4. Managed entry writes only through the typed mutation surface

No entry surface performs a durable store write. Work capture reached through
a host-started session happens inside the session through the typed mutation
surface. C18's read-only construction survives this record in that form; its
entry-route scope does not.

## Consequences

- `clarifications.md` R1 records the supersession and then the retirement:
  the Concord entry route replaced ZLauncher and absorbed the bootstrap role,
  and the entry route retired in favor of the host start.
- `vertical-integration.md` cites this record for the interface direction.
- Operator acceptance of the deleted TUI and the removed entry route has no
  subject. No acceptance gate transfers to the host start or to managed
  entry.
- Documents that restate the R1 split (`design-constraints.md` §6,
  `workflows.md` §0, `feature-inventory.md` §3.10,
  `self-documentation.md` §1.1) align to this record during the build.

## Rejected alternatives

**Iterate on the shipped interactive launcher.** Rejected: the operator judged
the gap too wide, and issue 908 deleted the surface rather than iterating.

**Keep a Concord entry route beside the host start.** Rejected: session start
and resume belong to the host (CD-0078), and a second launch entry keeps that
responsibility in two owners.

**Port ZLauncher as it stands.** Rejected: predecessor law forbids routing
Concord work through predecessor paths. The replacement mapped features, not
code.

**A launch registry in the entry route.** Rejected: it duplicates store-owned
state and would drift.

**In-route work capture.** Rejected: it would make the entry route a second
write authority beside the typed mutation surface.

## Verification

- The manifest registers this record as an accepted decision.
- `clarifications.md` R1 and `vertical-integration.md` cite this record.
- `go test ./cmd/concord/ -run
  'TestRemovedLaunchEntriesRefuseWithoutExecuting|TestRemovedLaunchArgvZeroDoesNotForward'`
  proves the removed entry verbs reach no launch and start no process.
- Repository document, knowledge-index, and link validators pass.
