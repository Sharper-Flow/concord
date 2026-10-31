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
      "exemption": "FK-only corruption detection is a verification property. The guarantee is proved by TestBackupRejectsForeignKeyOnlyCorruption (internal/store/backup_test.go)."
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
      "exemption": "Tamper and schema-direction rejection is a verification property. The guarantee is proved by TestBackupVerifyTamperAndOlderSchemaRejection (internal/store/fence_test.go)."
    }
  ]
}
---
# Concord Backup, Restore, and Reclamation (PM10)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** PM10; Product-memory recovery amendment.
> **Binding inputs:** PM1–PM9, CD-0002 I1–I6, PM2 global SQLite authority,
> PM6 git notes, compaction-retention-policy projection pruning, PM8 no-CAS boundary, PM9 no-receipt boundary.
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
with verification and manifest rules, the six-step restore protocol with the
`.recover` emergency boundary, and the failure-handling and reclamation
table. Sections 4 through 7 record invariants, the clean-machine acceptance
drill, reopen criteria, and sources, and carry no obligation.
## 1. Decision

Concord backs up and restores exactly two authorities:

1. a consistent snapshot of the one local SQLite database; and
2. canonical git repositories containing durable notes.

WIP output, screenshots, blobs/CAS, process-exhaust receipts, derived SQLite
projections, caches, and temporary backup files are not authority and are not backup
inputs. The backup command uses SQLite's Online Backup API; copying a live `.db` file
or WAL is forbidden.

Each candidate snapshot passes `integrity_check`, `foreign_key_check`, and `quick_check`
before promotion. A disposable restore copy also passes supported forward migrations
and restore verification before promotion of the snapshot and its manifest.
This rule includes pre-upgrade snapshots. A failure prevents the guarded
mutation. Verification never changes the source database or the retained snapshot.
Git restores pass `git fsck --full`. A backup manifest records schema
version, snapshot ID, creation time, DB checksum, and git repository commit IDs. It is
an inventory, not a third authority.
Restore results distinguish source-snapshot inventory from the verified restored database
inventory after migration and verification. Neither inventory is authority.

Recovery restores a verified snapshot and reconciles current rows with Git,
worktrees, artifacts, and external effects. When recovery cannot reconcile an occupancy row, it refuses with a typed
refusal that names the worktree, the session, and the caller's action
([CD-0242 D11](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d11-a-fresh-session-resumes-from-current-state), [CD-0242 D12](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d12-every-refusal-states-an-action-the-caller-may-take)); no replay of retained events exists
([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

## 2. Restore protocol

1. create a clean recovery directory; never overwrite the live database in place;
2. restore and verify git repositories first;
3. verify the SQLite snapshot, atomically install it as the next local DB, and open it
   with accepted per-connection settings including `PRAGMA foreign_keys=ON`;
4. apply forward-only schema migrations; reject a snapshot newer than the
   installed binary's supported schema;
5. rebuild derived indexes from git; verify PM6 locators and run the PM1 corpus; and
6. swap the verified recovery directory into service atomically, otherwise retain the
   prior live state and report typed recovery failure.

`sqlite3 .recover` is emergency salvage only when no verified snapshot remains. Its
output is suspect, must be isolated, and cannot replace a verified restore without an
operator-approved recovery record.

## 3. Failure handling and reclamation

| Failure | Required behavior |
|---|---|
| backup interrupted | discard partial output; do not promote manifest; retry from a new snapshot |
| verification fails | quarantine candidate; retain live authority unchanged |
| restore interrupted | prior or verified-next state only; never expose partial state |
| git integrity failure | reject restore and surface affected repository/objects |
| projection corruption | restore the last verified snapshot, then re-verify |
| store corruption | restore last verified snapshot; `.recover` only last resort |

PM10 authorizes deletion only for disposable caches and completed, verified
temporary backup files. It never deletes canonical git history, the newest
verified snapshot, or a backup required by the configured retention policy
([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). In-place `VACUUM` is stopped-only;
online reclaim uses a fresh verified snapshot rather than a live-file copy. Git GC is
never concurrent with compaction writes and retains Git's configured grace window.

CD-0215 research retirement is governed by its explicit retirement operation,
not PM10 reclamation. Active pack tables are included in backups while present;
live deletion does not retroactively alter verified backups, which age out under the
ordinary retention policy.

## 4. Invariants

1. Only accepted SQLite and git authorities are required for clean-machine recovery.
2. Every promoted backup and restore passes SQLite triple verification; `integrity_check`
   alone is insufficient because it does not check foreign keys.
3. A recovery never serves a partial DB, mixed git/DB generation, or unverified
   derived index as complete.
4. Recovery restores verified snapshots; it never substitutes `.recover` output
   or event replay
   ([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
5. PM10 reclamation never deletes retained SQLite/git authority or asserts recovery
   for PM8/PM9-excluded data; CD-0215 explicit retirement separately deletes
   research authority.

## 5. Migration and acceptance proof

Schema manifests are versioned. Restore to a newer compatible binary applies
supported forward migrations and verifies; restore to an older unsupported binary
rejects before serving.
The production gate is an automated clean-machine drill that restores only the manifest,
SQLite snapshot, and git clones; rebuilds every disposable derived index; validates note
locators; and passes the PM1 query corpus with no hidden local state.

Required fault tests: concurrent-writer backup; interrupted backup/restore; corrupted
snapshot; foreign key (FK) only violation; missing git object; projection-only corruption; schema
version direction; compaction-prune recovery; safe temporary-file/projection GC; and
proof that no screenshot/log/CAS/receipt is required.

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
- Git object verification and GC: https://git-scm.com/docs/git-fsck and
  https://git-scm.com/docs/git-gc

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
  Then the snapshot is quarantined and never promoted.

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
- Criterion 3 is proved by `TestBackupRejectsForeignKeyOnlyCorruption`
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
  (`internal/store/fence_test.go`). Section 6 records the reopen criteria
  for each guarantee.
