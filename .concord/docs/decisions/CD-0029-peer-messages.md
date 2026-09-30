# CD-0029: Peer messages addressed to durable work

- **Status:** Accepted
- **Date:** 2026-08-15
- **Scope:** Agent tool surface; work-item events; continuity snapshot
- **Related:** CD-0014 (no polling), CD-0016 (continuity), CD-0028 (claims), issue #86

## Context

Under concurrent operation, one session routinely learns what another needs and
cannot observe: a deploy failure's cause, a trunk constant that moved, a fence
whose underlying state shifted. The only transport was the operator, who is
serial, lossy, and scales linearly with agent count. And obsolescence is
normal — the incident's prepared message was stale ten minutes after
authoring because its target rebased and self-resolved.

## Decision

**D1. Messages are durable events addressed to work, not sessions.**
`work.message_sent` carries a recipient work id in its payload; the
`work_messages` fold validates the recipient exists (the successor-link
precedent for cross-work folds) and writes one row per (sender, recipient)
pair. Whatever session next picks the work up reads the same rows — restart,
compaction, and context loss on the receiving side cannot lose a message,
because the message never belonged to the session.

**D2. Two addressing forms, one Product.** Direct: one recipient work id.
Broadcast: every `in_progress` work in the sender's ambient Product — the
fan-out resolves inside the sender's mutation transaction, excludes the
sender, excludes terminal work, and is capped at 100 recipients. Broadcast
never crosses Products; C18 §12 anti-requirement 11 stands untouched.

**D3. Delivery is pull-at-next-call.** No polling, timers, or daemons (CD-0014).
Two pull points: `concord_work_browse.messages` (PM1.Q14) returns the bounded
message list for a work item, and the continuity snapshot now carries
`pending_messages` — a sent-state count that points a resuming session at its
unread findings the way it is already pointed at checkpoints and pending
decisions. The pointer is re-derived per call, so it inherits CD-0016's
restart survival by construction.

**D4. No authority, structurally.** Nothing reads messages: no gate, no fold,
no transition consults them. A message cannot approve, complete, unblock, or
rewrite a contract because no code path gives it the chance; the test asserts
a recipient's terminal transition still demands operator approval after
receiving a message that claims it. Messages are also fully auditable: they
are ordinary domain events, visible in the operator's history read.

**D5. Obsolescence at read time.** Senders may withdraw a message; withdrawal
is a state change, never a deletion — reads return withdrawn messages
state-stamped so a reader sees both the finding and its retraction, and the
continuity pointer counts only sent messages. No acknowledgements, no seen
state, no timers: the reader evaluates staleness against current work state,
which the incident showed can resolve faster than delivery.

**D6. Bounded.** Body at most 4096 characters (the decision-rationale
precedent — enough for a finding with a reference, not enough to encourage
essays), reads capped at 100, retained rows capped by the fold-only table's
growth discipline. Messages reference durable state rather than copying
credentials, grants, or transcripts; the body bound enforces the pressure to
reference rather than replicate.

## Rejected alternatives

**Session addressing** — dies at the restart the architecture prefers.
**Acknowledgement/seen state** — chat-system drift the issue explicitly
forbids, and unread counts already serve the attention purpose.
**Push delivery** — would require the notice mechanism C18 §16 names as its
own future; pull-at-next-call satisfies the requirement without it.
**Cross-Product broadcast** — reopens an accepted anti-requirement for no
first-use case.

## Verification

`internal/agent/work_messages_dispatch_test.go`: direct + broadcast delivery
with a concurrent fixture (three active works, one terminal — broadcast
reaches exactly the active others, never the sender, never terminal work);
continuity pending-count before and after withdrawal; withdrawal visible as
state, not absence; and the no-authority assertion — a message claiming
operator approval does not soften the recipient's approval requirement, and
the message remains in the event log for operator audit.
