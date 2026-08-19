# CD-0023: Verdict read scope for the executing actor

- **Status:** Accepted
- **Date:** 2026-08-14
- **Scope:** Agent tool surface; workflow verdict reads
- **Related:** CD-0006 (D8 single operator), CD-0008 (D5 external conditions),
  CD-0013 (D5 verdict distinctness), CD-0017 (D4 worker boundary), issue #125

## Context

The predecessor enumeration (`docs/predecessor-operational-coverage.md` §3)
requires an acceptance verdict from "an authority the implementing agent cannot
read or influence". Concord held the influence half: CD-0013 D5 refuses a
verdict whose agent and session refs equal the recorded executing values, and
CD-0017 D4 keeps workers from recording verdicts at all. The read half did not
exist: verdict content reached no agent surface at all, so no session —
executing or not — could audit an acceptance, and the executing session was not
distinguished from any other reader.

CD-0006 D8 fixes Concord as a permanently single-operator system. One SQLite
authority is OS-readable by the operator by design, so "cannot read" can only
mean *through Concord's authority surface* — the same boundary that holds the
grant-time trunk firewall.

## Decision

**D1. The recorded verdict is a first-class, read-scoped result.** A terminal
work item's recorded verdict (`terminal_state`, `final_verdict_kind`,
`verdict_actor_ref`, `impact_verdict`, `completed_at`) is returned by
`concord_work_browse.scope` as a `verdict` object, so any session other than
the executing one can audit an acceptance.

**D2. The recorded executing actor cannot read the verdict it was judged by.**
When the reading grant's agent and session refs equal the executing actor
recorded on the workflow instance, the `verdict` object is withheld and an
explicit `redacted` omission (`executing_actor_verdict_read_scope`) is
returned. Redaction is never silent.

**D3. Influence remains write-side and unchanged.** CD-0013 D5 and CD-0017 D4
own the influence boundary. This decision adds no write rule.

## Rejected alternatives

**Cross-work-item verdict authority.** Amending CD-0008 D5 so an external
condition could await an authority operation on another work item would let an
isolated evaluation workflow own the verdict. It widens a deliberately closed
rule, and without D2 it would not deliver "cannot read" anyway — the
implementing session could still browse the evaluation work. Nothing in the
floor requires a separate verdict-producing workflow.

**Cross-process or cross-installation conformance.** Closest to the
predecessor's externally-isolated conformance, but for a permanently
single-operator system the isolation is theater: the operator is root on both
sides. It adds identity agreement between two authorities and is not
authorized by any floor condition.

**Deliberate exclusion.** Recording that distinctness suffices would leave the
verdict unauditable by every session, which is a worse reading of the same
enumeration.

## Consequences

- The executing session learns that its work is terminal (work summary already
  carries lifecycle) but not the verdict's kind, author, or impact assessment.
- Verdict reads are omitted only for the executing actor tuple; every other
  session, including review and verification lanes, reads the full verdict.
- The operator's launcher reads the store directly and is unaffected.
- `work_scope` in `contracts/agent-tool-surface-payloads.schema.json` gains the
  optional `verdict` object; the generated manifest digest remains the only
  pre-go-live agent-surface identity.

## Verification

- `internal/agent/verdict_scope_test.go`: the executing session's scope read
  omits the verdict and records the redaction omission; a distinct session
  reads the full verdict with no omissions. The executing identity is recorded
  by driving the real engine to the external repair step — not by seeding the
  projection directly.
