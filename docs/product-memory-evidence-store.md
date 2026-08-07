# Concord WIP Evidence and Blob Scope (PM8)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** PM8; binding CD-0002 §2d amendment.
> **Binding inputs:** PM1, accepted PM2–PM7, CD-0002 I1–I6, and the
> one-operator/one-machine envelope.
> **Operator decision:** WIP needs no durable byte-level proof. Do not introduce
> hashing or a content-addressed blob store without a named user job. Screenshots are
> not required generic evidence.
> **Related accepted boundary:** PM9 rejects a separate process-exhaust receipt.
> **Does not decide:** PM10 backup/restore/GC,
> C14/C15, TS1–TS9, artifact encryption, or external-system retention.

## 1. Decision

Concord v1 has **no evidence/blob content-addressed store**. It does not hash, ingest,
deduplicate, retain, back up, or garbage-collect WIP logs, traces, screenshots, test
output, recordings, or arbitrary binary files.

WIP output is process exhaust owned by its producing tool or environment. It must not
become a Concord domain event, durable evidence reference, external-file path record,
or a backup obligation merely because it exists. A failed task may still record a
bounded outcome/error classification in normal Product-memory state; raw output is not
required for that state to be valid.

No generic Concord task, workflow, acceptance, or compaction rule may require a
screenshot. A product-specific contract may require direct human visual inspection, but
PM8 does not turn that into screenshot capture, upload, retention, or hashing.

This narrows CD-0002 §2d: its proposed external evidence files with “hash + path” are
not a v1 Concord feature. Small, structured Concord artifacts remain SQLite text under
the existing decision; durable markdown knowledge remains PM6/PM7 git content. Neither
is a general binary-evidence store.

CD-0009 narrows one structured-artifact case further: active research-pack text lives
only in retention-bounded SQLite pack tables while its Epic/change/research owner is
active. It never enters retained `domain_events` or Git as a research pack and is
deleted after proof-backed archive. Selected durable decisions/specs/lessons/reasoning
are promoted to their existing PM6/CD-0006 homes before deletion; that promotion is
not pack retention.

### Accepted synchronization

This accepted change retires the previous external-blob assumption across:

- CD-0002 §2a's blob-placement framing, §2d, its evidence-blob falsifier, and §7's
  “Closed earlier by this record” evidence-placement clause;
- CD-0003's `blobs(...)` spine row;
- PM2 backup/export wording that currently names CAS or an evidence manifest;
- PM3's `artifacts / blobs` row;
- compaction-design's §2d dependency and live-tier blob diagram;
- PM1's external-blob budget exception; PM6/PM7 PM8 deferrals; the PM6 dependency
  cell; the PM8 and PM10 rows in both clarification tables; and the TS7 durable-ID/
  hash claim and PM8 dependency;
- README and storage-slice cross-references.

The accepted edits remove only the retired Concord blob mechanism. They preserve normal
PM3 `external_refs` for PRs, commits, upstream systems, and URLs; those are ordinary
locators, not retained WIP-byte paths.

## 2. Named value and boundary

The selected value is **less development-flow friction and fewer false retention
promises**:

- developers and agents keep using their normal test runner, CI, editor, artifact, and
  screenshot facilities without an extra upload/hash/attach protocol;
- a work item can progress or compact without proving long-lived access to transient
  bytes;
- Concord avoids a second filesystem durability, corruption, recovery, backup, and
  deletion subsystem whose value is not currently named;
- PM7 keeps event history and PM6 keeps concise reviewed knowledge, which are the
  current post-hoc needs—not exact WIP byte replay.

The boundary is structural: Concord's v1 schema and domain-event registry contain no
blob descriptor, evidence-reference, file-path, digest, content-addressed-store, or
blob-lifecycle event. A workflow cannot accidentally make WIP output durable by adding
an optional metadata field.

## 3. Normal flow and failure handling

| Situation | Concord behavior | Not a Concord responsibility |
|---|---|---|
| test/build/log output | producer/CI retains or discards it under its own policy | ingestion, hashing, mirroring, or backup |
| task fails | record only the bounded state/outcome needed by its owning Product-memory event | storing full command output as evidence |
| terminal compaction | PM6 note distills outcome, decision, lesson, and normal links | copying raw WIP files into git/SQLite |
| PM7 projection pruning | retains authoritative `domain_events` and durable note/index behavior | preserving raw WIP bytes |
| producer output disappears | surface only an external-link failure if a user tries to follow a normal link | recovery/substitution of a Concord-held blob |

No operation may report an unavailable WIP blob as an integrity incident, because
Concord never claimed to hold that blob. Likewise, failed output capture must not block
a domain operation unless that operation's independently accepted contract requires a
bounded result field that cannot be produced.

## 4. Migration, backfill, retention, and recovery

There is no evidence-store migration or backfill. Existing WIP paths, attachments, and
large outputs are not scanned, hashed, copied, or enrolled into Product memory.

There is no Concord blob retention period, refcount, quarantine, backup inventory, or
garbage collector. PM10 therefore backs up the accepted SQLite/git authority only; it
does not inherit a CAS subtree from PM8. Accepted PM9 keeps process findings in concise
durable knowledge when material and rejects a separate receipt—not automatic retention
of raw WIP bytes.

## 5. Structural invariants

1. **No undeclared byte authority:** no Concord table, event, or durable note is a
   canonical store for arbitrary WIP bytes.
2. **No hidden retention:** paths, digests, payload copies, and serialized command
   output cannot enter generic metadata as a workaround for the omitted store.
3. **Bounded state only:** task/work outcomes retain only fields accepted by their
   owning domain contract; raw output remains outside that contract.
4. **No implied recovery promise:** Concord restores only its accepted authorities, not
   data held by a test runner, CI provider, operating system, or user workspace.
5. **Explicit promotion only:** if future work needs a durable artifact, it must first
   name its reader, decision/recovery job, authority, retention duration, and restore
   guarantee in a new accepted decision.
6. **Research expires:** CD-0009 active pack rows are not WIP-byte evidence or durable
   knowledge; archive may retain selected reasoning in normal outputs, then deletes
   the complete pack with no tombstone or hidden history.

## 6. Required implementation-acceptance scenarios

1. A large WIP test log and screenshot complete without any Concord blob table, digest,
   file-path metadata, or backup entry being created.
2. A failed task stores a bounded failure classification/state while raw tool output
   remains with the producing environment.
3. PM6 compaction creates a concise note with outcome/lesson/ordinary links and no raw
   output copy or generated evidence attachment.
4. PM7 prunes an eligible work projection while Q7 event history and the note/index
   remain valid without requiring any external blob.
5. Schema and event-registry validation reject attempts to introduce blob descriptors,
   content hashes, arbitrary filesystem paths, or raw output payloads through generic
   metadata.
6. PM10 clean-machine restore succeeds with SQLite and canonical git authorities alone;
   no absent CAS root is reported as degraded or missing.

## 7. Alternatives rejected

### Content-addressed CAS now

Rejected. It solves exact-byte identity, deduplication, corruption detection, and
restore only when someone must later retrieve the same large evidence bytes. Concord
has no accepted job requiring that guarantee. Adding it now would burden normal WIP
flow and create an unearned backup/GC surface.

### Hash every WIP output “just in case”

Rejected. Hashing alone creates identity-looking metadata without a reader, retention
promise, or recovery value. It is observability noise, not product memory.

### Inline raw output in SQLite or git notes

Rejected. It recreates the process-exhaust dump that CD-0002 and compaction design
explicitly reject, increases authority/backup weight, and does not name a future-reader
benefit.

### Store caller-selected external file paths

Rejected. Paths are mutable environment references, not durable identity. Recording
them would imply a recovery contract Concord cannot satisfy.

## 8. Explicit deferrals

- **PM10:** backs up/restores only accepted Concord authorities; no CAS or blob GC is
  introduced here.
- **C14/C15:** no presentation, Product field, managed-resource, or ownership rule.
- **TS1–TS9:** no command, tool, agent schema, permission, or transport surface.
- external CI/artifact provider retention, encryption, artifact signing, remote blob
  storage, chunking, media processing, and binary provenance.

## 9. Reopen criteria

Reopen PM8 only when evidence demonstrates a concrete recurring job that needs all of:

1. a named reader who must retrieve exact large bytes after the producing WIP context
   is gone;
2. a decision, audit, incident, or recovery outcome that cannot use bounded state,
   canonical git knowledge, ordinary external links, or the producer's own retention;
3. an explicit authority and retention/restore promise; and
4. measured frequency/value sufficient to justify ingestion, integrity, backup, and
   eventual deletion complexity.

Any future proposal must name the job before selecting CAS, hashes, an external provider, or
any other storage mechanism.

## 10. Research basis

PM8 reviewed the previous CAS option against official SQLite external-blob guidance,
SQLite atomic-commit behavior, NIST SHA-256, OCI content descriptors, and Linux
install/durability primitives. Those sources validate how a CAS *could* work; they do
not establish a Concord user need. The accepted PM1–PM7 contracts and operator
direction control this decision.
