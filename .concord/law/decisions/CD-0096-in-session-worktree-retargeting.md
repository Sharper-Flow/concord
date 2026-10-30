# CD-0096: Same-Project inspection and verification have separate authority

- **Status:** Accepted
- **Date:** 2026-09-01
- **Scope:** Same-Project inspection, exclusive native verification, and continuity leases
- **Approval:** The operator approved the tiered authority model in session on 2026-09-01 (issue #689).
- **Related:** CD-0088, CD-0090, CD-0092, CD-0093, CD-0094, CD-0104

**Approval amendment:** [CD-0243 D6](CD-0243-four-roles-and-six-operator-approval-classes.md#d6-existing-approval-requirements-follow-these-classes)
limits this record's operator-consent requirements to its six classes; its D5
governs recovery. Identity and other current non-consent checks remain required.

## Decision

### D1. The host places the session

There is no stored effective target or Concord retarget operation. Host tools
place the session under CD-0088 D1. Later Concord operations resolve the tree
from the directory the host reports per call.

### D2. Verification and inspection targets come from identity, never input

Native verification and inspection accept no caller-selected worktree path.
Their subjects derive from the ambient Project and exact work identity through
retained entry metadata. Host worktree creation and path policy belong to
CD-0088 D1, not these reads and verification operations.

### D3. Same-Project cross-worktree authority is tiered

Concord grants two bounded same-Project authorities. Neither grants editing,
transfer, or deletion authority.

**Inspect.** Reading files, Git status, and diffs is allowed against any active
worktree in the same Project. Inspection is read-only and never moves the
session.
The `oracle_output` mode reads retained native-verify output for the same work
and ambient Project through its producer run reference, without an active tree.
It starts no process, creates no lease, and records no mutation or page bytes.
Each page contains at most 16 KiB of raw bytes within the existing envelope.

**Verify.** Tests, validators, builds, and reproductions run against a
same-Project worktree under an exclusive lease. Tracked files must remain
unchanged. Completion refuses when they changed, because a verifier that edits
its subject verifies nothing.
Native oracle preparation and execution use this same lease and operation.
Preparation compiles the pinned harness without launching a test program.
Execution selects the exact retained preparation_run_ref and its declared
bundle, pinned harness, candidate and environment under
[the workflow engine contract](../specs/workflow-engine-contract.md#51-tables).
The producer stages private snapshots without changing the claimed worktree.
The lease retains one immutable canonical producer plan and its digest.
It stores one raw stdout `BLOB` and one raw stderr `BLOB`, each at most 2 MiB.
Bounded result metadata contains descriptors, not raw stream bodies.
No separate output store, dispatch authority, or evidence kind is created.
Existing scope, budget, lease, and mutation-refusal gates remain mandatory.

Host tools own editing placement and deletion under CD-0088 D1. Concord
provides no takeover, reclaim, or destroy tier.

### D4. Possession grants no Product authority

Holding or targeting a worktree changes no capability boundary. Authority
still flows only from the typed operation grants CD-0092 and CD-0094 define.
Moving a session into a worktree grants no Product authority.

### D5. Continuity re-pins the lease

The pinned continuity projection carries the reading session's active verify
leases.

### D6. Preparation is not ownership

`session-prepare` retains CD-0093's directory, identity, and registry checks
and records nothing. It creates no worktree and records no landing.

## Verification

- Inspection of any active same-Project worktree succeeds read-only.
- Verification under an exclusive lease refuses completion when tracked files
  changed.
- The continuity projection re-pins the active lease each turn.
- Knowledge, documentation-link, and generated contract checks retain these
  inspection and verification obligations.
