# CD-0208: Exact worker-evidence acknowledgment appends no new evidence

- **Status:** Proposed (accepted standing begins at pull-request merge)
- **Date:** 2026-10-06
- **Scope:** Exact existing-event acknowledgment and retained completed-Task recovery
- **Amends:** CD-0044 D2 and D4; the consumed-window boundary in CD-0059 and CD-0067
- **Preserves:** Current caller authentication, nonce protection, immutable worker
  outcomes, single-use dispatch authorization, original packet and provenance,
  original model identities, CD-0207 consequential commits, and CD-0038 caller budgets
- **Related:** [CD-0044](CD-0044-worker-evidence-caller-authentication.md),
  [CD-0059](CD-0059-worker-dispatch-action.md),
  [CD-0067](CD-0067-dispatch-worker-packet.md),
  [CD-0207](CD-0207-a-durable-commit-replaces-the-checkpoint-barrier.md),
  [CD-0038](CD-0038-per-operation-seconds-budgets.md),
  [CD-0159](CD-0159-authority-tier-for-product-law.md), and Concord (CON)
  [issue 842](https://linear.app/sharper-flow/issue/CON-842/durability-checkpoint-failure-blocks-session-preparation-and-strands-a)
- **Approval:** The operator approved contract version 1 for
  `work-eee5ffe324a3c303eab87703` on 2026-10-06. Its first outcome names CD-0208
  as the legislated deliverable. The typed approval returned `ok` at work
  version 13 on the repair surface. The approval authorizes the legislative
  deliverable only: this record claims no report acceptance, work completion,
  or standing as accepted law before its pull request merges.

## Context

Worker evidence can commit while its acknowledgment is lost. An exact retry
then meets a consumed dispatch window or a terminal attempt.

CD-0044 D2 ties nonce consumption to an evidence append. D4 refuses further
evidence after a terminal outcome. Neither clause names acknowledgment of an
event that already exists.

This record defines that acknowledgment. It grants no authority to rerun a
worker, overwrite a result, or fabricate evidence.

## Decision

### D1. An exact acknowledgment is not new evidence

An authorized caller can request acknowledgment of an existing worker event by
its original event identifier. The following fields must equal the stored event:

- Event kind.
- Subject type and identifier.
- Verified actor.
- Payload version.
- Complete payload.

Occurrence time is not payload identity. A later observation time changes
neither the original event nor its recorded time.

A mismatch refuses. A match returns the original identifier and appends no
second worker event. It changes no outcome, workflow step, verdict, or acceptance.

### D2. Current authentication binds every acknowledgment

The caller supplies a fresh signed assertion under current client policy.
The verified client and principal must equal the stored actor. The caller
still needs the worker-evidence capability.

A reused or stale assertion refuses. A revoked client or key, missing
capability, or changed principal also refuses. Key rotation grants no exception.

The consequential transaction consumes the fresh nonce together with the exact
match. It appends no evidence event. This is the bounded exception to CD-0044
D2's append requirement.

An ordinary refusal rolls back nonce consumption. The acknowledgment is not
a new historical evidence event or a separate review record. Nonce retention
uses the existing replay window.

### D3. A consumed window grants no authority to run again

Acknowledgment requires the original recorded authorization for the same work,
attempt, and packet digest. It also requires the original claimed Project and
worktree identity.

The original window can already be consumed. An exact acknowledgment neither
consumes it again nor reopens or replaces it.

A missing, empty, or conflicting original digest refuses. Fresh execution and
verdict correction still require their own admitted window and packet.

### D4. A terminal outcome remains immutable

The core worker-evidence verbs can acknowledge an exact existing dispatch,
completion, or failure event. Such a match is not further evidence under
CD-0044 D4.

A terminal attempt still refuses a new event, another outcome, or changed
payload. Acknowledgment of an existing failure neither completes nor reopens
the attempt.

The public retained-Task recovery route is narrower. It recovers the original
completed Task on an originally dispatched or completed attempt. It refuses a
failed attempt.

### D5. Retained recovery reads original proof

The recovery route derives its authorization and event identifiers from the
core. It reads the following original proof through trusted host records:

- Parent session and completed Task part.
- Child session and opening packet.
- Executing agent and report.
- Original model identities and claimed directory.

The caller cannot replace the report, packet, model, provenance, or signature.
Missing or conflicting proof refuses.

The recorded dispatch model and original terminal model can differ. Each
original record remains unchanged. Recovery adds no model-routing rule.

### D6. An uncertain commit is not an acknowledgment

The CD-0207 consequential transaction owns durability for the match and nonce.
Visibility of an old event alone proves no successful acknowledgment.

A possible committed effect remains uncertain until its declared reconciliation
completes. A failure reports no success. The CD-0038 caller budget spans the
complete invocation without a renewed phase allowance.

### D7. An original identifier bounds this permission

The permission in D1 requires the original event identifier and exact proof.
It does not promise automatic recovery for every adapter write path. It grants
no authority to replace an unknown identifier.

## Alternatives considered

- **Reuse the original assertion.** Rejected because a consumed nonce stays consumed.
- **Append a second evidence event.** Rejected because it adds evidence no worker produced.
- **Reopen the window or rerun the Task.** Rejected because acknowledgment grants no fresh execution authority.
- **Trust event visibility or replacement proof.** Rejected because neither proves authenticated durable acknowledgment of original evidence.
- **Store a separate acknowledgment event.** Not selected because the receipt carries no new review or workflow authority.

## Consequences

Exact recovery requires no weaker authentication or mutable outcome. It does
not supply a workflow verdict or accept the historical report.

Stable failure-event identifiers remain separate follow-up scope for born-failed
dispatch, invalid-report closure, and abandoned-attempt fallback. This record
does not promise automatic recovery for those paths.

The deterministic evidence covers lost acknowledgments and commit-phase
uncertainty. It does not inject a physical filesystem or fsync fault.

## Verification

- `cmd/concord.TestWorkerEvidenceExactReconciliation` proves exact dispatch,
  completion, and failure acknowledgment and refuses nonce reuse.
- `cmd/concord.TestWorkerEvidenceReconciliationRefusesChangedPayloadAndRevokedCaller`
  proves changed evidence and revoked callers refuse.
- `cmd/concord.TestWorkerRecoveryContextOriginalIdentity` proves original
  identity and the failed-attempt recovery boundary.
- `internal/store.TestValidateRecoveryPacket` rejects malformed or conflicting packets.
- `internal/store.TestDurableCommitErrorPreservesPossibleEffect` proves truthful
  uncertain-commit classification without claiming physical fault injection.
- `cmd/concord.TestWorkerEvidenceDurableReconciliationIgnoresPinnedReader` proves
  the existing FULL owner admits reconciliation under a pinned WAL reader.
- `bun test adapter/opencode/worker_recovery.test.ts adapter/opencode/dispatch_route_end_to_end.test.ts`
  proves original host proof, distinct model records, repeated acknowledgment,
  and no new Task, with host Product selection unset.
- `bun test adapter/opencode/concord.test.ts` proves the controlled one-second
  deadline interrupts a cooperative two-second read within 1,600 ms without
  worker evidence, with host Product selection unset.
- `python3 scripts/check-knowledge-index.py` and
  `python3 scripts/check-knowledge-closure.py` check current hashes and complete registration.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-cd-allocation.py`,
  and `python3 scripts/check-law-coverage.py` check declared structure.
  These checks do not replace behavioral evidence.
