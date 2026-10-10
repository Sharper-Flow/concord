# CD-0148: Failed worker retries require exact approval and fresh fencing

- **Status:** Accepted
- **Date:** 2026-09-14
- **Scope:** Worker lane dispatch and workflow correction
- **Amends:** CD-0017, CD-0027
- **Related:** CD-0013, CD-0059, CD-0102

## Context

A failed worker attempt is terminal evidence. A retry that reuses its identity,
epoch, or host session can apply a result to the wrong execution boundary.
Approval that names only the work item does not prove which failed attempt the
operator reviewed. A retry also needs a durable bound to the approved contract.

## Decision

After a worker attempt fails below the escalation wall, the agent mutation
boundary must challenge for an exact operator decision. The challenge binds
the failed attempt ID, failed attempt epoch, active contract version, work
version, scope, and request digest.
The failed attempt remains immutable and terminal.

Only the matching, unused, unexpired approval can admit `dispatch_worker`. The
retry must use a new attempt ID and the next fenced step epoch. A missing, stale,
expired, or reused approval refuses without a new attempt or dispatch event.
The host deletes any resume task identity when it consumes a dispatch window.

The existing limit of three correction attempts remains in force. A
correction escalates when a comparator of CD-0164 D4 reaches the limit or
when the nonprogress wall of CD-0164 D1 reaches three. The work pin keeps
`dispatch_worker` visible under the escalation reason of CD-0173 D1. The
wall is basis-gated, not a dead end.

A store-derived convergence basis alone admits a `dispatch_worker` against
an escalated correction, and the boundary mints no retry approval challenge
at the wall. The store derives the basis in the same transaction that folds
the dispatch. Without a derivable basis the fold refuses the dispatch with
`missing_evidence` and mints no challenge. An operator approval neither
substitutes for a basis nor opens the wall.

The basis families are closed. Findings convergence requires a latest open
findings set that is non-empty and a strict subset of the previous comparable
reject `open_finding_ids`. The finding identifiers are stable, and the set
holds 1 to 32 of them. On oracle-free histories, correction predicates
require the `request_correction` predicate ids inside the open window.
On oracle-capable histories, rejection and correction records carry the exact
derived ranked-finding set; predicate IDs do not substitute for finding IDs.
A latest failure supplies no findings
basis, because a failure carries no findings. A contract supersession after
the latest dispatch at any step is a changed approach, and a changed
approach is a basis.

Oracle finding IDs resolve through terminal event sequence and ordinal
(CD-0197). Comparability retains the approved contract, job identity, owner
obligations, and every previously required control byte-identically, including
the pinned recipe, arguments, expected result, and evidence role. Each dropped
finding requires supported resolution evidence. Added cases and controls can
strengthen coverage. A new uncovered variant stays in the latest set, so
replacement is not shrinkage. Receipt preservation alone is not progress.

An oracle-defect closure supplies no `findings_shrinking` basis. A changed or
re-pinned required recipe invalidates comparison and cannot mint shrink credit;
escalation then requires the existing explicit contract-supersession
`approach_changed` basis. Accepted `no_ship` findings remain debt, not productive
acceptance or a third basis family. Historical pins retain their comparison.

Each basis admits exactly one fresh fenced attempt, and the admitted
dispatch consumes its basis. Findings convergence compares records at one
step, so a step change yields no reusable findings basis. Dispatch
completion durably records the derived basis token (`findings_shrinking` or
`approach_changed`) and the sequence references of the events the basis was
derived from. A convergence dispatch opens no new window: the correction
stays escalated until a productive acceptance resets the nonprogress budget
(CD-0164 D2). Below the
limit nothing changes: a failed disposition keeps the approval-gated retry,
and a rejected completed result keeps its ordinary correction dispatch
without a second approval. A worker cannot record workflow transitions,
verdicts, completion, or resume a failed worker session.

## Consequences

- The approval challenge exposes the failed attempt and contract bindings.
- A valid retry creates a distinct worker attempt and step epoch.
- Invalid approval and retry identity changes have no durable workflow effect.
- A dispatch without a basis refuses with `missing_evidence`, mints no
  challenge, and an operator approval has no effect.
- An escalated correction admits a dispatch only behind a store-derived
  convergence basis, and the exact approval decision stays a below-limit
  mechanism.
- Dispatch completion records the derived basis and its sequence references,
  so every escalated dispatch carries the basis that admitted it.
- CD-0027's fresh dispatch path remains stateless and does not become typed restart.

## Verification

- Agent mutation tests prove the challenge, fresh identity, stale approval,
  reused approval, and retry-limit boundaries
  (`internal/agent.TestWorkerRetryMutationRequiresExactApprovalAndFreshAttempt`,
  `internal/agent.TestGenericOneOffBelowWallFailedRetryKeepsTheExactApproval`).
- Agent mutation tests prove the escalated wall mints no challenge, a derived
  basis admits one fresh fenced attempt, a dispatch without a basis refuses
  with `missing_evidence`, the refusal holds behind an approval, and the wall
  re-arms (`internal/agent.TestEscalatedFailedCorrectionWallAdmitsOnlyBehindApproachConvergence`,
  `internal/agent.TestGenericOneOffEscalatedWallAdmitsOnlyBehindStoreConvergence`).
- Agent mutation tests prove the findings basis admits one escalated retry on
  the correction and verification paths
  (`internal/agent.TestGenericOneOffFindingsConvergenceAdmitsOneEscalatedRetry`,
  `internal/agent.TestEscalatedVerificationCorrectionWallGatesOnFindingsConvergence`).
- Store tests prove failed-attempt immutability, fresh attempt identity, fresh
  epoch fencing, and the three-attempt limit.
- Store tests prove the escalated dispatch fold refuses without a derivable
  basis, admits exactly the one dispatch the basis buys, and records the
  derived basis and sequence references at dispatch completion
  (`internal/store.TestWorkflowFourthCorrectionDispatchRefusesWithApprovalRequired`,
  `internal/store.TestConvergenceBasisAdmitsTheSameStepDispatch`,
  `internal/store.TestHalfMaterializedDispatchConsumesTheConvergenceBasis`).
- Adapter tests prove approval forwarding and removal of a resume task identity.
- Adapter tests prove approval forwarding stays below the limit, and a
  `missing_evidence` refusal fail-closes without an operator ask.
- `TestOwnerOracleConvergence` and `TestOwnerOracleControlsRetained` cover supported strict subsets, recipe re-pins, and oracle-defect closures without widening the wall.
