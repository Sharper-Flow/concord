# Legacy relation receipt recovery

## Question and scope

Which retained relation attributions can be proved after migrations 146, 150
and 151, and what would withholding unproven attribution cost? This is research
for [CON-1909](https://linear.app/sharper-flow/issue/CON-1909/legacy-relation-history-can-still-attribute-an-internally-consistent)
and [PR 2442](https://github.com/Sharper-Flow/concord/pull/2442), against base
`4089016f43cae541f588de1ef742b30d2f8da277`. It makes no law, runtime change,
production-data change, or approval request.

A supplied native backup was checked read-only; no live store was opened. The
observation is dated, not a current population or writer-identity cutoff. The
private assessment belongs to the work record linked from the issue. Earlier
contradictory joins are not a current count, and rows that passed the filter are
not thereby established as correct.

The assessed backup has no `domain_events` table or pre-151 payloads. It can
restore saved stored metadata, not true facts missing from the backup. This
research does not complete the owning issue's live-data decision or treatment;
the operational route is outside this slice.

## Mechanism

Migration 146 assigns dense relation positions to selected events in event
order, not from the old relation IDs. Those selected events include some that
need not leave a retained edge. Old IDs can also have gaps from other history.
See [schema.go:7215–7223](../../../internal/store/schema.go#L7215-L7223).
The relation table that migration 146 sees keeps its autoincrement IDs and
high-water mark across rebuilds ([schema.go:6215–6228](../../../internal/store/schema.go#L6215-L6228)).
Neither coordinate's numeric value establishes identity with the other.

Migration 150 obtains each edge's reason and version from the event whose
receipt position equals the old relation ID, without checking the endpoints
or event identity ([schema.go:7431–7437](../../../internal/store/schema.go#L7431-L7437)).
Migration 151 drops the event log and payloads while retaining receipts and
overlap-resolution facts ([schema.go:7458–7479](../../../internal/store/schema.go#L7458-L7479),
[schema.go:7520–7529](../../../internal/store/schema.go#L7520-L7529)).
Migration 196 advances the allocation frontier; it does not repair prior
attribution ([schema.go:9335–9343](../../../internal/store/schema.go#L9335-L9343)).

Q7 joins an edge ID to receipt `relation_seq`, rejects duplicate positions and
several contradictions, and requires a positive version. It does not compare
the missing payload's target or establish that an otherwise matching event
created this edge ([q7_current.go:188–215](../../../internal/store/q7_current.go#L188-L215)).
The reader does not label a surviving attribution uncertain. A zero version
omits the row from Q7; it does not establish whether the original mapping was
corrupted. Q8 can still serve the edge independently; the existing
[Q7/Q8 test](../../../internal/store/q7_relation_attribution_test.go#L109-L129)
shows the separate read boundaries.

Current insertion uses the event's reserved receipt position and writes its
metadata with the edge ([lifecycle.go:698–727](../../../internal/store/lifecycle.go#L698-L727),
[lifecycle.go:747–755](../../../internal/store/lifecycle.go#L747-L755)).
That writer's soundness does not identify which retained rows it wrote. A row
needs independently verified writer identity and evidence that no manual writes
altered it. Matching ID and receipt position or timestamp alone does not prove
the construction. The observation time is not a writer-identity cutoff.

## Classification

**PROVABLE** means evidence identifies the particular insertion, not merely a
compatible event. Proof is field-specific:

* A trustworthy pre-151 artifact can bind an event ID and complete payload to
  its surviving receipt using the canonical payload digest
  ([effect_receipts.go:17–24](../../../internal/store/effect_receipts.go#L17-L24)).
  The digest covers the payload only; the receipt keeps effect position, kind,
  subject, actor and payload version but no event time
  ([schema.go:7168–7177](../../../internal/store/schema.go#L7168-L7177)), so an
  artifact's time is not bound.
  It must also uniquely bind that insertion to the retained edge. Check event
  headers, endpoints, kind, time, and the full history needed to exclude
  removal/re-addition or another indistinguishable insertion. A matching digest
  proves candidate content, not by itself which insertion survives.
* A consistent direct overlap link from the edge's `resolution_id` through the
  retained resolution's `event_seq` to its receipt can identify the creating
  event and actor, even when the numeric relation position misjoins, provided
  the legacy writer's provenance establishes that it linked the inserting
  event. The resolution independently corroborates endpoints, kind and time;
  using its reason as the true reason also depends on writer provenance.
  Neither the resolution nor receipt retains the resulting version. A
  mismapped version therefore needs other evidence.
* A Q7-admitted overlap row pins its event twice: the edge's `resolution_id`
  equals the receipt's event ID, and the retained overlap resolution names the
  receipt's effect position, endpoints, kind, reason and time
  ([q7_current.go:211–214](../../../internal/store/q7_current.go#L211-L214)).
  Its `record_version` came from the v150 numeric join, but that join read the
  same receipt: migration 146 positions were unique, later positions come from
  a monotonic sequence ([events.go:117–127](../../../internal/store/events.go#L117-L127)),
  and receipts are immutable after 151
  ([schema.go:7521–7522](../../../internal/store/schema.go#L7521-L7522)). The
  version is therefore that event's payload value. This rests on the legacy
  writer having set `resolution_id` from the inserting event; no migration
  backfills it. This is conditional source reasoning; no Q7-admitted overlap
  rows in the dated assessment does not disprove it.
* An independently verified exact writer identity, together with evidence that
  its invariants held and no manual writes altered the row, may establish the
  current-writer mapping. No such production cutoff is verified.

**UNPROVEN** includes a numeric match, a single Q7 result, consistent surviving
discriminants, and a plausible reason or version without the identity evidence
above. Endpoint/kind/time agreement is insufficient when duplicates or
remove/re-add histories are possible. A digest is not a decryption route, but
a fully specified candidate payload can be hash-validated; the lost payload is
not necessarily information-theoretically unrecoverable. Unpinned rows are
unproven from the retained evidence assessed, not unprovable from every possible
source. A fully specified digest-validated candidate plus unique insertion, or
a trustworthy pre-151 artifact with the necessary identity evidence, could
establish more; neither was established in the assessment. No evidence here
authorizes deleting an edge.

## Synthetic evidence

[relation_recovery_research_test.go](../../../internal/store/relation_recovery_research_test.go)
contains disposable fixtures, not production observations. The historical
fixture starts at schema 144, advances through 150, and migrates to current.
The test scaffolding copies the disposable fixture and supplies its checksum and
state digest, which the migration rechecks before capture retirement; it skips
the upgrade command's backup verifier. This is not evidence of a production
backup.
A synthetic ID gap shifts a retained edge against two same-source/kind
events; the intended first event and the numeric second event have distinct
actors, reasons and versions. The test showed Q7 displaying the second event's
metadata on the first event's edge, while Q8 retains the edge. Independently
authored payloads are checked against receipt digests after retirement.

The fixture then clears the selected edge's reason and sets version to zero.
Q7 omitted the entry, Q8 and all receipt columns remained intact,
and restoring the saved values restored the previous attribution, not its
truth. Receipts are immutable by trigger, so the receipt check confirms only
that the edit reaches no receipt. At the base commit, Q7 is the only non-migration
reader of the relation `reason` and `record_version` columns. A second fixture
hand-inserts two same-edge candidates whose payloads agree with the retained
edge's endpoints and kind and whose digests bind by construction; Q7 still
selects one only by numeric position. These are experiments, not a production plan.

Both synthetic tests passed independent verification. The verifier reused the
unchanged second test's earlier passing result. The commands are:

```sh
bin/oc-test targeted -- go test ./internal/store -run '^TestRelationRecoveryResearchShiftAndWithholding$' -count=1 -v
bin/oc-test targeted -- go test ./internal/store -run '^TestRelationRecoveryResearchDuplicateCandidates$' -count=1 -v
```

## Recommendation and draft approval question

Recommend withholding attribution only where a defined, evidenced assessment
cannot prove it, while preserving every edge and receipt and retaining proven
metadata. A pinned recoverable reason is separate from an unknown version.
Leaving unproven metadata unchanged avoids the proposed loss but can
continue to show plausible wrong attribution without an uncertainty label.

**DRAFT — not execution-ready; do not present as an approval yet.** First
establish the exact population and writer provenance on a fresh disposable
snapshot, identify a supported row-level execution and undo route, and verify a
fresh retained backup before treatment. Restoring a whole-store
backup would also revert every later write; undo needs a route that restores
only the saved values, on rows that still exist unchanged.

> **Production application data approval:** Once the affected records and
> recovery route are verified, should Concord stop showing the author, reason
> and version of older links whose origin cannot be proved, or leave the records
> unchanged? Concord would clear each unproven stored reason and mark its
> version unknown. Every link, the system's record of each change, and any
> proven detail would stay, including a recoverable reason whose version is
> unknown. I recommend clearing the unproven details. Link history will stop
> listing those links; the links themselves stay. Clearing loses the stored details, including any that happen
> to be correct. A fresh verified backup would let us put the stored values back,
> but cannot recover true facts it does not hold, and restoring the whole backup
> would also undo later work. Leaving the records unchanged avoids that loss but
> may continue to show a plausible wrong author as certain.

No data rewrite or production action is authorized by this draft. Do not ask
for approval of an unknown population; the owner must establish the population,
writer provenance and recovery evidence before routing any approval.
