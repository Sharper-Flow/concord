---
{
  "id": "CD-0142",
  "title": "Operator-directed execution removal preserves the planning record",
  "authority": {
    "tier": "legislated",
    "legislated_by": "work-8d7bd788b2890355460bb5b7",
    "contract_version": 1
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "PM4"
    },
    {
      "kind": "refines",
      "target_id": "CD-0217"
    }
  ]
}
---
# CD-0142: Operator-directed execution removal preserves the planning record

- **Status:** Accepted
- **Date:** 2026-09-14
- **Scope:** Shelving and cancellation of nonterminal Concord execution items, D6's shelving of an item already in lifecycle `cancelled`, liveness evidence, planning handoff, and replay-safe projection removal.
- **Approval:** The operator approved this bounded removal contract on 2026-09-14.
- **Related:** PM4, PM6, compaction-retention-policy, CD-0213, CD-0217
- **Preserves:** The five-state lifecycle, exact recorded removal receipts, Product and Project
  identity, accepted law, Linear issue identity, and the no-polling boundary

**Consent:** [CD-0243 D7](CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
qualifies every operator-consent clause in this record, including tables and
acceptance criteria. Other admission, safety, evidence, and historical-pin
requirements remain unchanged.

## Context

Shelving execution does not mean cancellation. Both actions must preserve the
current findings and remove the Concord execution identity from operational
projections. A hidden or sixth work state would leave an agent able to select or
resume the execution. A deletion without a recorded removal could return in
recovery; recovery restores verified snapshots and reconciles current rows ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

## Decision

### D1. Removal is a distinct typed operation

Concord keeps the closed lifecycle states `needed`, `in_progress`, `completed`,
`cancelled`, and `superseded`. Shelving is not a lifecycle state and does not
emit cancellation. Cancellation uses the same removal machinery with the reason
`cancelled`, but its reason remains distinct in the durable receipt it records.

The operation records `work.removed` and atomically removes the work item plus
its owned execution projections; the removal receipt is a current row ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). The durable receipt keeps
the exact audit handoff without exposing an actionable WorkItem.

A lifecycle transition to `cancelled` under its existing gates may precede exact-item terminal reclamation as preparation for shelving. It cancels the managed execution, not the planning goal, and supplies no completion credit. Removal retains reason `shelved`, emits no second cancellation and claims no completion. Neither preparatory action replaces the committed `work.removed` receipt.

Before removal, the caller preserves the verified cancellation identity, work version, actor, reason and evidence references, claim-generation retirement facts, retained branch references and recovery identities in the planning handoff. The caller verifies that handoff at its planning destination. When existing removal approval is required, the operator reviews its meaning through that approval. Existing shape, destination and confirmed-digest checks bind the content into the receipt; they do not infer meaning from prose.

### D2. Preparation fences execution and requires a complete handoff

Preparation pins an operation identity, idempotency key, work revision, reason,
and handoff digest. It refuses stale revisions,
occupied sessions or worktrees, pending writes, effects, claims, required
consumers, dependencies, and incomplete artifact or handoff evidence. Unknown
host state remains unknown and never grants removal authority.

The handoff contains findings, remaining scope, blockers, artifact references,
and renewal conditions. A final commit revalidates the gates in the same SQLite
transaction. No force removal or automatic process termination exists.

### D3. The planning destination remains the authority

Products using Linear for planning require a verified Product-scoped issue
identity and handoff digest before removal. The caller creates, adopts, or
updates the issue through the Linear MCP server and records its identity on the
work unit before confirming the handoff. An existing issue uses adoption or
update evidence. A missing issue uses one durable creation intent owned by the
caller. Ambiguous or failed planning writes stop local removal.

Concord checks the confirmation against the recorded issue identity and handoff
digest and makes no Linear call. Removal, including the caller's planning
writes for it, does not close, archive, or delete the issue.

Non-Linear destinations remain an explicit operator choice. Concord does not
invent a Git note, GitHub adapter, second database, or generic provider layer.

### D4. Absence survives reads, recovery, and renewal

Operational lists, exact-ID reads, launcher navigation, continuity, work start,
claims, and overlap computation exclude a removed ID. Audit reads return only
the explicit historical receipt. Snapshot restore, restart, migration, and
knowledge rebuild preserve the absence; recovery restores verified snapshots and reconciles current rows ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). A retry with the original identity returns the
same committed receipt and records no second removal.

Renewed work starts with a new Concord identity and contract. It may reuse the
same confirmed Linear issue without creating a duplicate or inheriting old
approvals, attempts, reservations, or checkpoints.

### D5. Liveness is read-time evidence

Liveness is derived from current host occupancy, bounded waits, failures and
progress, not inferred subagent execution. A work item without verified active
or waiting evidence reports
`unknown`; liveness never authorizes removal.

### D6. Complete shelving from the registered default checkout

Transition the item's lifecycle to `cancelled` under its existing gates, then request exact-item terminal reclamation under CD-0162, not an audit sweep. This is the caller's preparation sequence. D5 admission checks the item's resulting retirement state and native proof, not unrecorded direct-versus-audit provenance. Remove through CD-0094 D5 and this decision's existing per-item preparation, handoff confirmation, approval and atomic removal machinery.

Shelving completes only after committed removal and verified execution-identity absence. The planning issue and exact handoff survive. D2's safety, D3's destination checks and D4's absence and new-identity renewal rules remain required.

This route neither closes the planning goal nor enacts outside effects. It grants outside work no Concord authority, evidence, verdict or completion credit.

## Consequences

- Shelving removes execution while preserving the planning handoff and history.
- Cancellation removal leaves no actionable cancelled WorkItem.
- Terminal compaction remains a separate retention operation.
- Every removal is per-item, operator-directed, fenced, bounded, and replay-safe.

## Verification

- `internal/store.TestWorkRemovalDeletesExecutionAndRetriesIdempotently` verifies
  projection removal, audit retention, and idempotent retry.
- `internal/store.TestWorkRemovalRefusesMissingSafetyEvidence` verifies a required
  removal gate.
- `go test ./internal/store -run 'TestWorkRemoval|TestMigrateV80'`
  verifies removal and migration.
- `python3 scripts/generate-agent-contracts.py --check` verifies generated
  contract projections after this decision is indexed.
