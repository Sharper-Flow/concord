---
{
  "id": "PM9",
  "title": "Product memory process exhaust",
  "authority": {"tier": "derived"},
  "home_domain_id": "product-memory",
  "applies_to_domain_ids": [],
  "criterion_bindings": [
    {
      "criterion": 1,
      "exemption": "The absence of a receipt mechanism is structural. TestPM8AndPM9DeclareNoEvidenceOrReceiptStore (internal/store/pm8_pm9_absence_test.go) checks that boundary."
    },
    {
      "criterion": 2,
      "exemption": "Typed unavailability is a query property. The surviving query suite refuses unavailable reads (internal/store/query_test.go); integrated typed-unavailability coverage remains unmeasured."
    }
  ]
}
---
# Concord Process-Exhaust Boundary (PM9)

## Context

Raw development output belongs to its producer. Bounded findings and ordinary
links can inform work without making Concord an audit or byte-retention system.

## Contract

### 1. No process-exhaust store or receipt

Concord creates no separate process-exhaust store, audit receipt, exhaust hash
or deletion-attestation record. Subagent reports, tool logs, retry traces,
intermediate snapshots, test output and screenshots remain producer-owned
outside Concord. Producers retain or discard them under their own policy;
completion requires no publication or deletion attestation.

The store keeps current state and finite verified snapshots under CD-0242 D2.
Evidence bindings remain attributed and subject-bound under CD-0242 D8.
PM6 §1 owns reported-link completion and disposal of step
records. Recovery restores accepted SQLite and Git authorities, not producer-owned
WIP output.

### 2. Findings use their existing owners

Material findings, reasons and remaining risks belong in the existing work
record while work is active. Enduring requirements and choices use their
accepted specification and decision routes. Transferable lessons and
post-completion reflection use the independently reviewed lesson pull request
under [CD-0026 D1–D2](../decisions/CD-0026-learning-capture.md). No completion
note, salvage attestation or compaction operation gates those routes.

Concord neither imports raw output nor proves its deletion. No generic metadata
field or receipt-shaped payload may retain raw reports, artifact paths, digests,
screenshots or serialized process output. Ordinary external links do not confer
a restore promise. No generic screenshot obligation applies.

### 3. Failure and recovery

| Condition | Required behavior |
|---|---|
| a lesson needs durable publication | use CD-0026's ordinary reviewed pull request |
| producer output disappears | report unavailable; never invent an integrity or verified-deletion claim |
| a required bounded result cannot be produced | follow the owning operation's typed refusal and recovery |
| a later reader needs raw output | consult its producer; Concord cannot recover bytes it never retained |

There is no process-exhaust migration, backfill, retention window, inventory,
backup or garbage collector. Existing producer reports are neither enrolled nor
destroyed by Concord.

## Acceptance criteria

- Given a process-receipt field, table or record proposal
  When schemas and the registry validate
  Then the process-exhaust retention mechanism is rejected structurally.

- Given a later request for dropped raw output
  When Concord answers
  Then it reports unavailable without asserting verified deletion or an integrity failure.

## Verification

The structural absence check and unavailable-read boundary require evidence;
the criterion bindings do not claim full integrated coverage.
