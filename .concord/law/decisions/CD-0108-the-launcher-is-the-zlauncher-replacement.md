# CD-0108: The ZLauncher replacement retired with the session entry route

- **Status:** Accepted
- **Date:** 2026-09-03
- **Scope:** host-owned session start and resume, terminal placement, and the
  managed-entry write boundary
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
and started or resumed an OpenCode session with an optional prompt.
Session entry needs a clear owner, separate from managed work authority.

## Decision

### D1. Retired: the session entry route

No compatibility alias or fallback starts or resumes a session through
Concord. The host starts and resumes operator
sessions (CD-0078). A host-started session enters managed work through
`concord_work_start` (CD-0098).

### D2. Concord places nothing and derives no workflow position at entry

No Concord component creates, names, splits, focuses, or destroys a tab,
pane, window, or session (CD-0078 D1, CD-0163). Entry into managed work
derives workflow position from the store's canonical projection (CD-0016),
never from a terminal surface or a terminal session identifier.

### D3. Retired: the entry-verb feature set

Session-entry verb names appear only in negative CLI assertions, never in
published help or runtime dispatch.

### D4. Managed entry writes only through the typed mutation surface

No entry surface performs a durable store write. Work capture reached through
a host-started session happens inside the session through the typed mutation
surface.

## Consequences

- `vertical-integration.md` cites this record for the interface direction.
- No acceptance gate transfers to the host start or to managed
  entry.
- Documents that restate the R1 split (`design-constraints.md` §6,
  `workflows.md` §0,
  `self-documentation.md` §1.1) follow this record's host-owned session boundary.

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
