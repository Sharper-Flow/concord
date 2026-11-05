---
{
  "id": "PM10",
  "title": "product memory recovery",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "product-memory",
  "applies_to_domain_ids": [],
  "criterion_bindings": [
    {
      "criterion": 1,
      "exemption": "Backup mechanics are not scenario-observable. The guarantee is proved by TestBackupUsesOnlineSnapshotAndPM10Manifest (internal/store/backup_test.go) and TestBackupAndRestoreRoundTripViaCLI (cmd/concord/main_test.go)."
    },
    {
      "criterion": 2,
      "exemption": "Interrupted-backup behavior is an operation property. The guarantee is proved by TestInterruptedBackupRemovesPartialSnapshotAndManifest (internal/store/backup_test.go)."
    },
    {
      "criterion": 3,
      "exemption": "FK-only corruption detection is a verification property. Rejection is proved by TestBackupRejectsForeignKeyOnlyCorruption; deletion of the failed candidate and its manifest is proved by TestInterruptedBackupVerificationDiscardsSnapshotAndStaging (internal/store/backup_test.go)."
    },
    {
      "criterion": 4,
      "exemption": "Operation-only checks: TestRestoreStageRejectsCurrentStateTampering (internal/store/backup_current_state_test.go); TestBackupRestoreDoesNotInterpretHistoryPayloads and TestInterruptedBackupVerificationDiscardsSnapshotAndStaging (internal/store/backup_test.go); TestUpgradeSnapshotFailureCommitsNothingAndClosesOwnedFence (cmd/concord/upgrade_snapshot_cli_test.go)."
    },
    {
      "criterion": 5,
      "exemption": "Restore staging and promotion is an operation property. The guarantee is proved by TestRestoreBackupUsesCleanOnlineRestoreAndPromotesVerifiedDatabase, TestRestoreBackupRefusesExistingDestinationUntouched, and TestInterruptedRestoreRemovesStagingAndLeavesDestinationAbsent (internal/store/backup_test.go)."
    },
    {
      "criterion": 6,
      "exemption": "Tamper and schema-direction rejection is a verification property. The guarantee is proved by TestBackupVerifyTamperAndOlderSchemaRejection (internal/store/atomic_operation_test.go)."
    }
  ]
}
---
# Concord Backup, Restore, and Reclamation (PM10)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** PM10; Product-memory recovery amendment.
> **Binding inputs:** PM1–PM9, CD-0002 I1–I6, PM2 global SQLite authority,
> PM6 historical notes, compaction-retention-policy, PM8 no-CAS boundary, PM9 no-receipt boundary.
> **Does not decide:** remote replication provider, encryption/key custody, retention
> schedule, TS1–TS9, or a new blob store.

[CD-0243 D7](../decisions/CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
qualifies every operator-consent clause in this record, including tables and
acceptance criteria. Other admission, safety, evidence, and historical-pin
requirements remain required.

## Context

One global SQLite authority and canonical git notes need one backup and
restore law. The binding inputs are PM1 through PM9, CD-0002 invariants,
PM6 git notes, compaction-retention-policy retention, and the PM8 and PM9 exclusion boundaries.
This record fixes recovery: exactly two backup authorities, the Online
Backup API with triple verification, the forward-only restore protocol, and
reclamation that never deletes retained authority.
## Contract

The binding contract is sections 1 through 3: the two-authority decision
with verification and manifest rules, the four-step restore protocol with the
`.recover` emergency boundary, and the failure-handling and reclamation
table. Sections 4 through 7 record invariants, acceptance proof, reopen
criteria, and sources, and carry no obligation.
## 1. Decision

Concord's recovery spans exactly two authorities:

1. the one local SQLite database, which Concord backs up and restores; and
2. canonical git repositories containing durable notes, which host tools back
   up, restore, and integrity-check. Concord records their commit IDs as
   manifest inventory and runs no git command during backup or restore.

WIP output, screenshots, blobs/CAS, process-exhaust receipts, derived SQLite
projections, caches, and temporary backup files are not authority and are not backup
inputs. The backup command uses SQLite's Online Backup API; copying a live `.db` file
or WAL is forbidden.

Each candidate snapshot passes `integrity_check`, `foreign_key_check`, and `quick_check`
before promotion. A disposable restore copy also passes supported forward migrations
and restore verification before promotion of the snapshot and its manifest.
This rule includes pre-upgrade snapshots. A failure prevents the guarded
mutation. Verification never changes the source database or the retained snapshot.
A backup manifest records schema
version, snapshot ID, creation time, DB checksum, and git repository commit IDs. It is
an inventory, not a third authority.
Restore results distinguish source-snapshot inventory from the verified restored database
inventory after migration and verification. Neither inventory is authority.

Recovery restores a verified snapshot and reconciles current rows with Git,
artifacts, and external effects. Host tools own checkout recovery; Concord
retains no worktree metadata or inspection service. Restore follows the
snapshot-backed table retirement in CD-0088 D2 and does not recreate ownership
or locator machinery. No replay of retained events exists
([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

## 2. Restore protocol

1. stage into a clean temporary staging file beside the destination; never
   overwrite the live database in place;
2. verify the SQLite snapshot, restore it into the staging file with the Online
   Backup API, and open it with accepted per-connection settings including
   `PRAGMA foreign_keys=ON`;
3. apply forward-only schema migrations; reject a snapshot newer than the
   installed binary's supported schema; and
4. promote the verified staging file to the destination by atomic no-replace
   link; any failure creates no destination, leaves any existing file and the
   live database untouched, and reports a typed recovery failure.

The protocol rebuilds nothing from git and probes no git repository: it verifies
the snapshot's retained current state and applies migrations. Resolving a PM6
locator against git belongs to the consumer
([PM6 §3](canonical-git-note-placement.md#3-historical-locator)), never to the restore.

`sqlite3 .recover` is emergency salvage only when no verified snapshot remains. Its
output is suspect, must be isolated, and cannot replace a verified restore without an
operator-approved recovery record.

## 3. Failure handling and reclamation

| Failure | Required behavior |
|---|---|
| backup interrupted | discard partial output; do not promote manifest; retry from a new snapshot |
| verification fails | delete the failed candidate and its manifest; retain live authority unchanged |
| restore interrupted | prior or verified-next state only; never expose partial state |
| store corruption | restore last verified snapshot; `.recover` only last resort |

PM10 authorizes deletion only for disposable caches and completed, verified
temporary backup files. It never deletes canonical git history, the newest
verified snapshot, or a backup required by the configured retention policy
([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). In-place `VACUUM` is stopped-only;
online reclaim uses a fresh verified snapshot rather than a live-file copy.
Git GC retains Git's configured grace window.

Existing research rows must be exported to files and verified before their tables
are dropped. The upgrade uses a verified snapshot from which the source remains
recoverable; an export failure aborts before the drop.

## 4. Invariants

1. Only accepted SQLite and git authorities are required for clean-machine recovery.
2. Every promoted backup and restore passes SQLite triple verification; `integrity_check`
   alone is insufficient because it does not check foreign keys.
3. A recovery never serves a partial or unverified database as complete.
4. Recovery restores verified snapshots; it never substitutes `.recover` output
   or event replay
   ([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
5. PM10 reclamation never deletes retained SQLite/git authority or asserts recovery
   for PM8/PM9-excluded data.

## 5. Migration and acceptance proof

Schema manifests are versioned. Restore to a newer compatible binary applies
supported forward migrations and verifies; restore to an older unsupported binary
rejects before serving.
Restore acceptance is the round-trip proof through the real CLI
(`TestBackupAndRestoreRoundTripViaCLI`, `cmd/concord/main_test.go`): a
manifest and snapshot pair restores on a clean destination with no hidden
local state. No derived-index rebuild, note-locator validation or query
corpus runs at restore.

Required fault tests: concurrent-writer backup; interrupted backup/restore; corrupted
snapshot; foreign key (FK) only violation; restore-stage current-state tampering;
schema version direction; safe temporary-file GC; and proof that no
screenshot/log/CAS/receipt is required.

## 6. Reopen criteria

Reopen if the one-file snapshot cannot meet measured recovery objectives, clean-machine
restore needs an omitted authority, a required recovery point objective (RPO) or recovery time objective (RTO) needs continuous replication, or a
safe deletion job needs retention/backup behavior this decision does not specify.
Any replacement must name its recovery job, authority, failure mode, retention, and
restore proof before introducing a daemon, remote store, or blob mechanism.

## 7. Sources

- SQLite Online Backup API: https://sqlite.org/backup.html
- SQLite integrity, FK, and quick checks: https://sqlite.org/pragma.html
- SQLite recovery caveats: https://sqlite.org/recovery.html
- SQLite VACUUM / `VACUUM INTO`: https://sqlite.org/lang_vacuum.html
- Git GC grace windows: https://git-scm.com/docs/git-gc

## Acceptance criteria

- Given a backup command
  When it runs
  Then it uses the Online Backup API, records a PM10 manifest, and never
  copies a live database or WAL file.

- Given a backup interrupted mid-run
  When it fails
  Then partial output is discarded and no manifest is promoted.

- Given a candidate snapshot with a foreign-key-only violation
  When verification runs
  Then the failed candidate is deleted and never promoted.

- Given a candidate snapshot that fails restore verification
  When backup or a pre-mutation snapshot runs
  Then no snapshot manifest is promoted and the guarded mutation does not start.

- Given a restore into an existing database
  When it runs
  Then it restores through a clean staging directory, verifies, and promotes
  atomically, leaving the destination untouched on any failure.

- Given a tampered or newer-schema backup
  When restore verifies it
  Then it is rejected before serving.

## Verification

No corpus scenario exercises backup machinery, so every criterion carries a
typed exemption naming the backup test that proves the guarantee.

- Criterion 1 is proved by `TestBackupUsesOnlineSnapshotAndPM10Manifest`
  (`internal/store/backup_test.go`) and
  `TestBackupAndRestoreRoundTripViaCLI` (`cmd/concord/main_test.go`).
- Criterion 2 is proved by
  `TestInterruptedBackupRemovesPartialSnapshotAndManifest`
  (`internal/store/backup_test.go`).
- Criterion 3 is proved by `TestBackupRejectsForeignKeyOnlyCorruption`;
  deletion of the failed candidate and its manifest is proved by
  `TestInterruptedBackupVerificationDiscardsSnapshotAndStaging`
  (`internal/store/backup_test.go`).
- Criterion 4 is proved by
  `TestRestoreStageRejectsCurrentStateTampering`
  (`internal/store/backup_current_state_test.go`),
  `TestBackupRestoreDoesNotInterpretHistoryPayloads` and
  `TestInterruptedBackupVerificationDiscardsSnapshotAndStaging`
  (`internal/store/backup_test.go`),
  `TestUpgradeSnapshotFailureCommitsNothingAndClosesOwnedFence`
  (`cmd/concord/upgrade_snapshot_cli_test.go`).
- Criterion 5 is proved by
  `TestRestoreBackupUsesCleanOnlineRestoreAndPromotesVerifiedDatabase`,
  `TestRestoreBackupRefusesExistingDestinationUntouched`, and
  `TestInterruptedRestoreRemovesStagingAndLeavesDestinationAbsent`
  (`internal/store/backup_test.go`).
- Criterion 6 is proved by `TestBackupVerifyTamperAndOlderSchemaRejection`
  (`internal/store/atomic_operation_test.go`). Section 6 records the reopen criteria
  for each guarantee.
