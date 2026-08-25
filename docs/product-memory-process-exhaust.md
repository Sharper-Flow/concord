# Concord Process-Exhaust Salvage and Receipt (PM9)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** PM9; binding compaction-design/CD-0002 amendment.
> **Binding inputs:** PM1 Q7/Q9/Q10, accepted PM2–PM8, CD-0002 I1–I6,
> PM6 publish proof, and PM7 retention.
> **Does not decide:** PM10 backup/restore, C14/C15, TS1–TS9, external CI retention,
> or a durable WIP-byte store.

## Context

Compaction design deferred a salvage-and-receipt handoff for process
exhaust, and CD-0002 carried a process-exhaust rule that assumed one. The
binding inputs are PM1 Q7/Q9/Q10, the accepted PM2 through PM8, PM6 publish
proof, and PM7 retention. This record closes the deferral: the existing
durable sequence — terminal events, the approved PM6 note, the verified
link, the optional prune event — is the only receipt, and process exhaust
stays producer-owned with salvage into bounded note fields before approval.
## Contract

The binding contract is sections 1 through 3: the no-receipt decision with
its required synchronization, the salvage rule placing material findings in
existing bounded note fields before approval, and the structural boundary
with its failure-handling table. Sections 4 through 9 record migration
absence, invariants, acceptance scenarios, rejected alternatives, deferrals
and reopen criteria, and research basis, and carry no obligation.
## 1. Decision

Concord creates **no separate process-exhaust store, audit-receipt object, hash, or
process-exhaust deletion-attestation event** in v1. The existing durable sequence is
the only receipt needed:

1. terminal `domain_events` history records the work outcome;
2. the approved PM6 note records outcome, why, decisions, and transferable lessons;
3. PM6's verified compaction-link event records the canonical note locator/proof; and
4. PM7's optional prune event records the later projection boundary.

Process exhaust—sub-agent reports, briefing digests, tool logs, retry traces,
intermediate snapshots, WIP test output, and screenshots—remains producer-owned and
outside Concord. It may be discarded by that producer after the PM6 durable sequence
commits. Concord does not discover, copy, enumerate, attest to, retain, or prove
deletion of those bytes.

### Required acceptance synchronization

This accepted change closes the receipt/salvage deferrals across:

- CD-0002's process-exhaust rule and compaction-design's salvage/receipt handoff;
- PM6 and PM7 header/scope/Q7/retention-table PM9 pointers;
- PM8's PM9 receipt-versus-durable-knowledge deferral;
- README, storage-slice, lifecycle, and clarification PM9/PM10 status wording; and
- the PM9 clarification lean, explicitly recording the operator's choice of no
  separate receipt because the listed fields already exist in accepted authorities.

## 2. Named value and salvage rule

The value is **one useful durable summary without a second audit system**. A future
reader needs to know what happened, why it mattered, key decisions, and reusable
lessons—not every failed tool call or raw log line.

Before PM6 approval, the note drafter must inspect available terminal context and place
each material transferable finding in the existing bounded note fields:

- outcome / why it mattered;
- one to three load-bearing decisions and reasons;
- transferable lessons; and
- ordinary links to commits, PRs, decisions, or external systems when useful.

The operator's existing PM6 approval attests to the useful summary. It does not certify
that every WIP artifact was read, captured, or deleted. A missing raw report is not an
integrity failure because Concord never promised to retain it.

## 3. Structural boundary and failure handling

No PM9 table, event kind, generic metadata field, or git template field may carry raw
process output, artifact paths, digests, serialized reports, or a mutable receipt
status. This is enforced by the accepted PM3 JSON boundary, PM6 concise-note template,
and PM8 no-WIP-byte-store boundary.

| Condition | Required behavior |
|---|---|
| material lesson discovered before note approval | include it in the note or a linked decision; do not attach raw output |
| note drafting/approval fails | compaction/linkage does not commit; retry the existing PM6 flow |
| git commit succeeds but SQLite linkage fails | follow PM6 orphan-note recovery; do not create a PM9 receipt |
| producer deletes output early | no Concord recovery or integrity incident; preserve bounded conclusion if still needed |
| later reader needs missing raw output | report it unavailable and evaluate PM9 reopen criteria; never fabricate a receipt |

PM7 pruning remains blocked by its existing PM6 proof/eligibility checks. PM9 adds no
new retention or deletion precondition and does not alter authoritative Q7 history.

## 4. Migration, backfill, and retention

There is no process-exhaust migration, backfill, retention window, inventory, backup,
or garbage collector. Existing reports and traces are neither enrolled nor destroyed by
Concord. PM10 restores accepted SQLite and git authorities only.

Older concise notes may be improved through an ordinary new git commit when a real
transferable lesson is discovered. That is knowledge improvement, not recovery of a
missing receipt, and never requires reconstructing WIP bytes.

## 5. Invariants

1. **One durable answer:** terminal outcome/knowledge comes from the existing event,
   note, and locator sequence—not a second receipt authority.
2. **Salvage before approval:** material reusable findings belong in reviewed durable
   text or a linked decision before compaction completes.
3. **No raw-output smuggling:** reports, paths, hashes, screenshots, and command dumps
   cannot enter generic metadata, notes, or receipt-shaped fields.
4. **No deletion fiction:** absence of WIP output is never represented as a verified
   deletion or an integrity failure.
5. **No implicit screenshot rule:** neither salvage nor acceptance requires screenshots.

## 6. Required implementation-acceptance scenarios

1. A terminal work item with a material finding produces an approved PM6 note containing
   the bounded lesson, then compacts without a PM9 table/event/receipt.
2. A long sub-agent report, test log, retry trace, and screenshot cannot be persisted in
   generic metadata, the git note, or a process-receipt field.
3. Failure before PM6 approval/linkage leaves the existing work state recoverable through
   PM6; no synthetic audit record is created.
4. PM7 pruning preserves Q7 event history and note locator behavior with no dependency on
   producer-owned WIP output.
5. A later request for dropped raw output returns unavailable rather than a false
   integrity/deletion assertion.

## 7. Alternatives rejected

### Separate audit receipt

Rejected. The terminal event, approved note, and verified PM6 locator already answer
the named audit question. A new receipt duplicates identifiers and creates another
schema, migration, and retention surface without a reader needing stronger proof.

### Hashes or deletion certificates for exhaust

Rejected. PM8 establishes that Concord has no WIP-byte authority. Hashing or certifying
deletion would create proof-shaped metadata without restore or reader value.

### Preserve every report until a retention window expires

Rejected. It recreates the Advance process dump and turns routine development output
into a storage, backup, and GC obligation.

## 8. Explicit deferrals and reopen criteria

PM9 does not decide PM10 recovery, external CI retention, C14/C15, or TS surfaces.

Reopen PM9 only when a recurring, named reader must determine a fact that the terminal
event, approved note, verified locator, ordinary external link, and bounded state cannot
answer; the proposal must state the required proof, retention period, restore promise,
and why a concise note cannot carry the finding before selecting a receipt mechanism.

## 9. Research basis

NIST SP 800-92 supports retaining only logging of greatest importance rather than more
data by default. SLSA distinguishes immutable source/provenance identifiers from a
general process-log archive. Those sources inform the minimal-audit boundary; accepted
PM1–PM8 and Concord's no-WIP-byte decision remain controlling.

## Acceptance criteria

- Given a terminal work item with a material transferable finding
  When the PM6 note is drafted and approved
  Then the finding lives in the bounded note fields or a linked decision,
  never in an attached raw report.

- Given any proposal of a process-receipt field, table, or event
  When the schema and registry validate
  Then it is rejected structurally.

- Given a later request for dropped raw output
  When Concord answers
  Then it reports the output unavailable without asserting verified deletion
  or an integrity failure.

## Verification

No corpus scenario can exercise the absence of a receipt mechanism, so every
criterion carries a typed exemption naming the structural proof.

- Criterion 1 is proved by the compaction flow tests of PM6's verification
  (`TestPublishCanonicalNoteCommitsOneNote` and
  `TestCompactionPayloadRequiresExplicitUniqueScopeArrays`,
  `internal/store/knowledge_index_test.go`), which fix the bounded note
  payload the operator approves.
- Criterion 2 is proved by
  `TestPM8AndPM9DeclareNoEvidenceOrReceiptStore`
  (`internal/store/pm8_pm9_absence_test.go`), which rejects receipt-shaped
  tables and event kinds by name over the whole registry.
- Criterion 3 is proved by `TestQueryQ10ReturnsAmbiguousAsTypedResult` and
  the typed-outcome tests of `internal/store/query_test.go`, which keep
  unavailable and integrity failures distinct. Section 8 records the reopen
  criteria for each guarantee.
