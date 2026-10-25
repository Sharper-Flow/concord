# Concord Process-Exhaust Salvage and Receipt (PM9)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** PM9; binding CD-0002 amendment.
> **Binding inputs:** PM1 Q7/Q9/Q10, accepted PM2–PM8, CD-0002 I1–I6,
> PM6 publish proof, and compaction-retention-policy retention.
> **Does not decide:** PM10 backup/restore, C15, TS1–TS9, external CI retention,
> or a durable WIP-byte store.

## Context

CD-0002 carried a process-exhaust rule that assumed a salvage-and-receipt
handoff. The
binding inputs are PM1 Q7/Q9/Q10, the accepted PM2 through PM8, PM6 publish
proof, and compaction-retention-policy retention. This record closes the deferral: closure
publishes one completion receipt, evidence is captured by its producer and bound
to its subject, and the store keeps current state and finite verified snapshots
with no event log or replay
([CD-0242](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) D2, D8, D9).
Process exhaust stays producer-owned with salvage into bounded note fields
before approval.
## Contract

For routine publication under CD-0013 D11, salvage and evidence review precede the exact durable publication intent, not a human approval. The authenticated publisher records its own summary attestation; no operator attestation is invented. Loss-prevention obligations, content/home proof, and research-retirement eligibility remain required. Physical research-pack deletion requires separate operator approval only when a CD-0243 D2 class applies. Publication acceptance does not itself permit research retirement.


The binding contract is sections 1 through 3: the no-receipt decision with
its required synchronization, the salvage rule placing material findings in
existing bounded note fields before approval, and the structural boundary
with its failure-handling table. Sections 4 through 9 record migration
absence, invariants, acceptance scenarios, rejected alternatives, deferrals
and reopen criteria, and research basis, and carry no obligation.
## 1. Decision

Concord creates **no separate process-exhaust store, audit-receipt object, hash, or
process-exhaust deletion-attestation event** in v1. Closure publishes one
completion receipt in the canonical knowledge home, linking the delivered
subject, the accepted outcome, consequential decisions, and the proof that
substantiates acceptance
([CD-0242 D9](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d9-completion-needs-proof-approvals-delivery-and-publication)).
Verification evidence is captured by its producer and bound to its subject, and
agents never re-enter it
([CD-0242 D8](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d8-evidence-is-captured-by-its-producer-and-bound-to-its-subject)).
The store keeps current state and finite verified snapshots, with no event log,
fold, or replay
([CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

External process exhaust—sub-agent reports, briefing digests, tool logs, retry
traces, intermediate snapshots, WIP test output, and screenshots—remains
producer-owned outside Concord. Its producer may discard it after the PM6
sequence commits. Concord neither imports those bytes nor proves their deletion.

Native oracle streams are core-owned verification observations under CD-0096 D3,
not imported external process exhaust. The existing verify lease retains one
bounded raw stdout `BLOB` and one raw stderr `BLOB` until lease/work deletion.
These bytes grant no knowledge, independent evidence, or acceptance authority.
They enter no generic metadata, concise note, or separate process-exhaust store.
This narrow native retention changes neither the completion receipt rule of
CD-0242 D9 nor the salvage obligation for external reports and traces. compaction-retention-policy owns its deletion boundary.

### Required acceptance synchronization

PM6 and compaction-retention-policy govern publication and retention.
The bounded note fields in section 2 carry salvage; no separate process-exhaust receipt is required.

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

The authenticated publisher's summary attestation under CD-0013 D11 records the useful summary after salvage and evidence review.
Routine PM6 publication requires no operator attestation; applicable operator approvals remain governed by CD-0243 D2.
The attestation does not certify that every WIP artifact was read, captured or deleted.
A missing raw report is not an integrity failure because Concord never promised to retain it.

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

compaction-retention-policy pruning remains blocked by its existing PM6 proof/eligibility checks. PM9 adds no
new retention or deletion precondition and leaves the store's current state and
finite verified snapshots unchanged
([CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

## 4. Migration, backfill, and retention

There is no process-exhaust migration, backfill, retention window, inventory, backup,
or garbage collector. Existing reports and traces are neither enrolled nor destroyed by
Concord. PM10 restores accepted SQLite and git authorities only.

Older concise notes may be improved through an ordinary new git commit when a real
transferable lesson is discovered. That is knowledge improvement, not recovery of a
missing receipt, and never requires reconstructing WIP bytes.

## 5. Invariants

1. **One durable answer:** closure publishes one completion receipt, and outcome
   knowledge comes from that receipt and the reviewed note—not a second receipt
   authority
   ([CD-0242 D9](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d9-completion-needs-proof-approvals-delivery-and-publication)).
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
4. compaction-retention-policy pruning preserves current-state and note locator behavior with no dependency on
   producer-owned WIP output.
5. A later request for dropped raw output returns unavailable rather than a false
   integrity/deletion assertion.

## 7. Alternatives rejected

### Separate audit receipt

Rejected. The completion receipt, approved note, and verified PM6 locator already answer
the named audit question
([CD-0242 D9](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d9-completion-needs-proof-approvals-delivery-and-publication)).
A new receipt duplicates identifiers and creates another
schema, migration, and retention surface without a reader needing stronger proof.

### Hashes or deletion certificates for exhaust

Rejected. PM8 establishes that Concord has no WIP-byte authority. Hashing or certifying
deletion would create proof-shaped metadata without restore or reader value.

### Preserve every report until a retention window expires

Rejected. It recreates the Advance process dump and turns routine development output
into a storage, backup, and GC obligation.

## 8. Explicit deferrals and reopen criteria

PM9 does not decide PM10 recovery, external CI retention, C15, or TS surfaces.

Reopen PM9 only when a recurring, named reader must determine a fact that the completion
receipt, approved note, verified locator, ordinary external link, and bounded state cannot
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
criterion carries a typed exemption naming the structural proof. The checker
`python3 scripts/check-doc-contract.py` validates those recorded exemptions.

- Criterion 1 is proved by the compaction flow tests of PM6's verification
  (`TestPublishCanonicalNoteCommitsOneNote` and
  `TestCompactionPayloadRequiresExplicitUniqueScopeArrays`,
  `internal/store/knowledge_index_test.go`), which fix the bounded note
  payload the publisher attests.
- Criterion 2 is proved by
  `TestPM8AndPM9DeclareNoEvidenceOrReceiptStore`
  (`internal/store/pm8_pm9_absence_test.go`), which rejects receipt-shaped
  tables and event kinds by name over the whole registry.
- Criterion 3 is proved by `TestQueryQ10ReturnsAmbiguousAsTypedResult` and
  the typed-outcome tests of `internal/store/query_test.go`, which keep
  unavailable and integrity failures distinct. Section 8 records the reopen
  criteria for each guarantee.
