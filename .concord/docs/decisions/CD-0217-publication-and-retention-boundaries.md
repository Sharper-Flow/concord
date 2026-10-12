# CD-0217: Publication and research retention have separate boundaries

- **Status:** Accepted
- **Date:** 2026-10-09
- **Approval:** `CON-816` A1 under operator delegation on 2026-10-09, without calendar expiration.
- **Supersedes:** [CD-0066](CD-0066-pm7-retention-amendment.md)
- **Related:** [compaction-retention-policy](../compaction-retention-policy-successor.md),
  [CD-0215](CD-0215-retained-research-context.md),
  [CD-0216](CD-0216-research-surface-and-explicit-retirement.md),
  [CD-0142](CD-0142-operator-directed-execution-removal.md), PM8, PM9, and PM10

## Context

Work publication and research retirement serve different purposes.
Git notes retain selected durable reasoning; direct-authority packs support exact revision-specific reuse.
Publishing a note must not destroy a pack or force its active consumers to stop.
Projection pruning remains a deferred optimization, not a new maintenance obligation.

## Decision

### D1. Terminal projection pruning remains deferred

No terminal-work prune eligibility predicate, storage-pressure threshold, maintenance run, or prune event is introduced.
Terminal work remains in live typed projections indefinitely.
`work_projection_pruned` and `archived_work_linked` remain reserved; introducing either requires a successor to this deferral.
CD-0142 remains a separate operator-directed nonterminal execution-removal boundary after verified handoff and reconciliation.
It is not terminal pruning and does not remove `domain_events`.

### D2. Authority guarantees remain in force

| Guarantee | Obligation |
|---|---|
| Git note | authority for distilled knowledge |
| `domain_events` | exact work history and replay; never pruned |
| Work IDs | immutable after a prune boundary; renewed need uses a new linked identity |
| Work projection removal | verified linkage precedes terminal removal; CD-0142 remains separate |
| Historical projections | disposable and Git-rebuildable |
| Live/historical queries | deduplicate by work ID and compose authority |
| Research content | direct-table authority retained until explicit retirement |
| Research retirement | terminal owner, zero active required or optional pins, authorized bounded version-fenced transaction |
| Publication | preserves research; does not require consumer release |

Research retirement needs neither a compaction link nor a calendar deadline.
Only `research_retire` deletes pack aggregates; eligibility alone has no destructive effect.
PM8's WIP-byte exclusion, PM9's process-exhaust rejection, and the ban on core-event pruning remain unchanged.

### D3. Live projection growth has a revisit trigger

Live work, relation, membership, idempotency, operation, worker, message, observation, and workflow projections may grow without bound at single-operator pre-floor scale.
A later accepted decision must restore a pruning mechanism before any of these conditions holds:

1. Representative projection size measurably degrades authoritative query latency, fold time, or startup.
2. Deployment moves beyond one operator through another concurrent operator or a hosted installation.
3. Backup snapshot size makes the absence of reclamation a recovery risk.

Research retirement does not supply that terminal-projection mechanism or change those triggers.

### D4. Backup retention remains separate

PM10 owns backup, restore, snapshot aging, and reclamation.
This decision introduces no `VACUUM`, snapshot deletion, secure-erasure promise, or new backup-retention duration.
`concord backup` continues to refuse an existing destination.
Research retirement does not delete earlier backup copies.

### D5. Deliberate absences stay absent

There is no core-event pruning, WIP-byte content-addressed store, separate process-exhaust receipt, calendar expiration, or fixed sweep.
The research aggregate remains outside retained events and historical work projections.
Work publication and read inspection cannot become alternate retirement routes.
Owner removal must preserve retained research or refuse before a foreign-key cascade can destroy it.

### D6. Coverage follows the replacement obligations

Historical archive-cleanup tests do not prove explicit retirement or publication without deletion.
Successor coverage must include preserved work-authority guarantees and the new research boundary witnesses.
The manifest must not carry forward a satisfied state based only on the archive-cleanup anchors.

## Alternatives considered

- Immediate terminal pruning introduces a deferred optimization without measured need.
- Research deletion during publication couples durable promotion to destructive cleanup.
- Calendar expiration substitutes elapsed time for explicit intent and consumer protection.
- Treating an old satisfied coverage record as current proof conceals changed obligations.

## Consequences

Terminal work remains queryable in live projections alongside Git-derived historical views.
Historical index rebuild remains derived, not a new work-authority writer.
Explicit research retirement adds bounded destructive intent without changing deferred work pruning.
Compaction note proof, exact history, frozen historical scope, and deduplicated query populations remain mandatory.

CD-0036 requires deployment to enumerate active CD-0066 and PM7 consumers and recover their contracts to the accepted successors, or cancel or supersede them.
This offline law edit does not establish any live consumer count or completed recovery.
The parent must integrate migration and replacement witnesses before deployment; no live store or activation belongs to this edit.

## Verification

Preserved anchors include `internal/store.TestCompactionLinkBoundaryRefusesStaleLawRevisionAfterClaim`
and `internal/store.TestQ10OrphanWorkNoteRemainsNotCompacted`.
New candidate anchors include `internal/store.TestResearchRetirementCascadesContent`
and `internal/agent.TestResearchRetireDryRunAndReplay`.
Publication, every terminal route, owner removal, restart/replay, restore, and the pin/retire race still need integrated assessment.
The successor coverage remains unmeasured rather than claiming that historical cleanup proves the new policy.
