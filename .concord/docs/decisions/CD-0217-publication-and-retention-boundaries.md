# CD-0217: Publication and research retention have separate boundaries

- **Status:** Accepted
- **Date:** 2026-10-09
- **Approval:** `CON-816` A1 under operator delegation on 2026-10-09, without calendar expiration.
- **Related:** [compaction-retention-policy](../compaction-retention-policy.md),
  [CD-0215](CD-0215-retained-research-context.md),
  [CD-0216](CD-0216-research-surface-and-explicit-retirement.md),
  [CD-0142](CD-0142-operator-directed-execution-removal.md), PM8, PM9, and PM10

## Context

Work publication and research retirement serve different purposes.
Git notes retain selected durable reasoning; direct-authority packs support exact revision-specific reuse.
Publishing a note must not destroy a pack or force its active consumers to stop.
Terminal execution cleanup follows CD-0242 D2; research retention remains a separate explicit boundary.

## Decision

CD-0013 D11 separates routine local publication/proof reconciliation from research retirement. Publication locks exact content, home, and work version in its durable intent without human attestation; it retains salvage and Git proof. Physical pack deletion requires exact operator approval only when a [CD-0243 D2](CD-0243-four-roles-and-six-operator-approval-classes.md#d2-the-operator-approves-six-classes-of-decision) class applies; dry-run and mere retirement eligibility grant no deletion authority. CD-0243 D5 governs recovery and permanent loss.


### D1. Terminal execution cleanup follows verified closure

Completed, cancelled and superseded execution state is eligible for cleanup only after verified canonical publication, release of active references and claims, and reconciliation of outstanding effects under CD-0242 D2.
The closure stage records the reviewed publication judgement required by CD-0242 D9; byte verification alone does not establish that judgement.
The canonical receipt links the delivered subject, accepted outcome, consequential decisions and acceptance proof.
Cleanup runs only after the publication operation has settled and removes no retained category that still has a consumer.
A reader of collected execution state receives typed absence and the canonical outcome locator, not a claim that the removed artifact remains inspectable.

Cleanup never shelves or cancels work and grants no authority to remove a nonterminal execution.
CD-0142 remains the separate scoped execution-removal route after its required planning handoff and reconciliation.
Terminal cleanup does not delete retained research; an owned research pack blocks cleanup until explicit eligible retirement through research_retire.
Releasing a research consumer pin never deletes its pack.
No work-removal operation or foreign-key cascade may delete retained research.

### D2. Authority guarantees remain in force

| Guarantee | Obligation |
|---|---|
| Git note | authority for distilled knowledge |
| Store state | current rows and finite verified snapshots; retention and cleanup follow [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) |
| Work IDs | immutable after a prune boundary; renewed need uses a new linked identity |
| Work projection removal | verified linkage precedes terminal removal; CD-0142 remains separate |
| Historical projections | disposable and Git-rebuildable |
| Live/historical queries | deduplicate by work ID and compose authority |
| Research content | direct-table authority retained until explicit retirement |
| Research retirement | terminal owner, zero active required or optional pins, authorized bounded version-fenced transaction |
| Publication | preserves research; does not require consumer release |

Research retirement needs neither a compaction link nor a calendar deadline.
Only `research_retire` deletes pack aggregates; eligibility alone has no destructive effect.
PM8's WIP-byte exclusion, PM9's process-exhaust rejection, and [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)'s retention rules remain unchanged.

### D3. Retention follows the consuming category

Each retained category names its consumer and removal condition under CD-0242 D2.
Terminal lifecycle alone neither preserves all execution projections indefinitely nor grants deletion authority.
Unpublished work, unresolved effects, active references and claims, and research protected by an owner or consumer remain outside destructive eligibility.
Cleanup has no calendar deadline, pressure threshold or periodic sweep.
Research retirement remains a separate authorized, bounded, version-fenced transaction.

### D4. Backup retention remains separate

PM10 owns backup, restore, snapshot aging, and reclamation.
This decision introduces no `VACUUM`, snapshot deletion, secure-erasure promise, or new backup-retention duration.
`concord backup` continues to refuse an existing destination.
Research retirement does not delete earlier backup copies.

### D5. Deliberate absences stay absent

There is no core-row pruning outside [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)'s documented rules, no WIP-byte content-addressed store, separate process-exhaust receipt, calendar expiration, or fixed sweep.
The research aggregate remains outside historical work projections.
Work publication and read inspection cannot become alternate retirement routes.
Owner removal must preserve retained research or refuse before a foreign-key cascade can destroy it.

### D6. Coverage follows the replacement obligations

Coverage must prove explicit retirement, publication without deletion, work-authority guarantees, and research boundary witnesses.
The manifest must not claim a satisfied state without evidence for those obligations.

## Alternatives considered

- Cleanup before settled publication or release of references removes evidence or coordination state that a consumer still needs.
- Research deletion during publication couples durable promotion to destructive cleanup.
- Calendar expiration substitutes elapsed time for explicit intent and consumer protection.
- Treating an old satisfied coverage record as current proof conceals changed obligations.

## Consequences

Retained terminal state remains readable without rewriting its recorded facts; collected execution state returns typed absence and its canonical outcome locator.
Historical index rebuild remains Git-derived, not a new work-authority writer.
Explicit research retirement remains separate from publication and terminal execution cleanup.
Canonical note proof, frozen historical scope and deduplicated query populations remain mandatory; CD-0242 D2 does not retain exact execution history.

Migration and conformance witnesses are required before deployment.

## Verification

Publication and research boundaries are checked by `internal/store.TestResearchRetirementCascadesContent` and `internal/agent.TestResearchRetireDryRunAndReplay`.
`internal/store.TestTerminalCleanupDefersUnreleasedOwners` checks unreleased ownership and effect blockers.
`internal/store.TestTerminalCleanupRetainsDefectAdmissionClassification` checks that recurrence-admission facts survive collection of execution state.
`internal/store.TestPublishedExecutionAbsenceReaders` and `internal/store.TestPublishedClassificationOnlyAbsenceReaders` check truthful readback after collection.
Publication, every terminal route, owner removal, restart, restore and the pin/retire race still require integrated evidence.
Coverage remains unmeasured until the required witnesses pass on the implementing subject.
