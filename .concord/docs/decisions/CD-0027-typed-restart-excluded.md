# CD-0027: Typed restart after a boundary is deliberately excluded

- **Status:** Accepted
- **Date:** 2026-08-15
- **Scope:** Context continuity and host session restart
- **Related:** CD-0016 (context continuity), issue #120
- Native Task belongs to the host ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-native-task-belongs-to-the-host)).

## Context

The predecessor enumeration (`.concord/docs/predecessor-operational-coverage.md` §3)
lists "restart cleanly into a typed agent after a boundary rather than
summarizing". Concord must preserve authority across context boundaries;
host session management owns working memory and subagent execution.

## Decision

**Deliberately excluded.** Restart-into-a-typed-lane will not be implemented,
and `typed_availability.restart` stays closed at `unavailable`.

The predecessor's restart existed to prevent silent loss of authority state —
law, approvals, workflow position, evidence — across a context boundary, by
re-dispatching the agent with its context rather than handing the next
generation a summary. Concord answers that problem structurally instead:
CD-0016's pinned continuity is durable, re-derived on every call, and never
summarized. A session that begins after a boundary receives the exact pinned
contract, workflow step, checkpoint, and pending operator decision — not a
paraphrase of them. Summaries are advisory by
accepted law.

What restart would additionally buy is continuity of *in-flight working
memory* — resuming mid-execution into the same lane with the same scratch
context. In a permanently single-operator system (CD-0006 D8), that belongs to
host session management, not Product authority. A new host session reads current
workflow authority through pinned continuity without a Concord relaunch,
lane registry or subagent-provenance claim.

Nothing in this record weakens CD-0016: checkpoints stay durable, boundaries
stay monotonic, pinned state stays re-derived per call, and restart stays
fail-closed with an honest reason.

## Consequences

- `WorkflowContinuitySnapshot` reports restart unavailable with this record as
  the reason.
- The enumerated outcome resolves to *deliberately excluded with an accepted
  reason*.
- If the operator later wants mid-execution lane resumption, the schema const,
  the snapshot reason, and this rule change together in one accepted change.

## Verification

- `internal/store/workflow_continuity.go` fails closed with the reason
  referencing this decision; `workflow_continuity_test.go` asserts
  unavailability with a non-empty reason.
- The payload schema keeps `typed_availability.restart` a closed `unavailable`
  const — the exclusion is structural, not a runtime choice.
