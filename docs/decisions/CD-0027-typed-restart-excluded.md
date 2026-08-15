# CD-0027: Typed restart after a boundary is deliberately excluded

- **Status:** Accepted
- **Date:** 2026-08-15
- **Scope:** Context continuity; worker lane dispatch
- **Related:** CD-0016 (context continuity), CD-0017 (typed workers), issue #120
- **Supersedes:** nothing; records a deliberate exclusion against the
  predecessor enumeration

## Context

The predecessor enumeration (`docs/predecessor-operational-coverage.md` §3)
lists "restart cleanly into a typed agent after a boundary rather than
summarizing". It was tracked under issue #57, lost its owner when #57 closed,
and was re-homed to #120 once the lane registry existed — because the
originally stated blocker ("typed-agent registry not implemented") had
vanished while restart remained hardcoded unavailable.

The mechanism is implementable today: the lane registry, the dispatch adapter,
and CD-0016's pinned checkpoints all exist. The question is whether the
capability it delivers is one Concord owes.

## Decision

**Deliberately excluded.** Restart-into-a-typed-lane will not be implemented,
and `typed_availability.restart` stays closed at `unavailable`.

The predecessor's restart existed to prevent silent loss of authority state —
law, approvals, workflow position, evidence — across a context boundary, by
re-dispatching the agent with its context rather than handing the next
generation a summary. Concord answers that problem structurally instead:
CD-0016's pinned continuity is durable, re-derived on every call, and never
summarized. A session that begins after a boundary receives the exact pinned
contract, workflow step, checkpoint, pending operator decision, and
unresolved failure — not a paraphrase of them. Summaries are advisory by
accepted law.

What restart would additionally buy is continuity of *in-flight working
memory* — resuming mid-execution into the same lane with the same scratch
context. In a permanently single-operator system (CD-0006 D8) that belongs to
the host's session management, not to Product authority: Concord's lanes are
stateless workers whose durable state is the workflow itself, and re-entering
execution through a fresh dispatch with pinned state is equivalent in
authority and cheaper in mechanism. Implementing restart would also require a
lane-selection authority rule (which lane may resume which step) whose only
purpose would be preserving what is already preserved.

Nothing in this record weakens CD-0016: checkpoints stay durable, boundaries
stay monotonic, pinned state stays re-derived per call, and restart stays
fail-closed with an honest reason.

## Consequences

- `WorkflowContinuitySnapshot` keeps reporting restart unavailable; the reason
  now cites this record rather than any pending work.
- The enumerated outcome resolves to *deliberately excluded with an accepted
  reason*; the coverage tally's excluded count rises by one.
- If the operator later wants mid-execution lane resumption, this record is
  superseded, not silently narrowed: the schema const, the snapshot reason,
  and this row change together in one accepted change.

## Verification

- `internal/store/workflow_continuity.go` fails closed with the reason
  referencing this decision; `workflow_continuity_test.go` asserts
  unavailability with a non-empty reason.
- The payload schema keeps `typed_availability.restart` a closed `unavailable`
  const — the exclusion is structural, not a runtime choice.
