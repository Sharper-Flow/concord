# Concord Backup, Restore, and Reclamation (PM10)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** PM10; Product-memory recovery amendment.
> **Binding inputs:** PM1–PM9, CD-0002 I1–I6, PM2 global SQLite authority,
> PM6 git notes, PM7 projection pruning, PM8 no-CAS boundary, PM9 no-receipt boundary.
> **Does not decide:** remote replication provider, encryption/key custody, retention
> schedule, C14/C15, TS1–TS9, or a new blob store.

## 1. Decision

Concord backs up and restores exactly two authorities:

1. a consistent snapshot of the one local SQLite database; and
2. canonical git repositories containing durable notes.

WIP output, screenshots, blobs/CAS, process-exhaust receipts, derived SQLite
projections, caches, and temporary backup files are not authority and are not backup
inputs. The backup command uses SQLite's Online Backup API; copying a live `.db` file
or WAL is forbidden.

Each candidate snapshot passes `integrity_check`, `foreign_key_check`, and `quick_check`
before promotion. Git restores pass `git fsck --full`. A backup manifest records schema
version, snapshot ID, creation time, DB checksum, and git repository commit IDs. It is
an inventory, not a third authority.

## 2. Restore protocol

1. create a clean recovery directory; never overwrite the live database in place;
2. restore and verify git repositories first;
3. verify the SQLite snapshot, atomically install it as the next local DB, and open it
   with accepted per-connection settings including `PRAGMA foreign_keys=ON`;
4. apply forward-only schema migrations/upcasters; reject a snapshot newer than the
   installed binary's supported schema;
5. rebuild typed projections from retained `domain_events` and historical indexes from
   git; verify PM6 locators and run the PM1 corpus; and
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
| projection corruption | rebuild from authoritative events/git, then re-verify |
| event-log corruption | restore last verified snapshot; `.recover` only last resort |

PM10 authorizes deletion only for disposable projections/caches and completed, verified
temporary backup files. It never deletes `domain_events`, canonical git history, or a
backup required by the configured retention policy. In-place `VACUUM` is stopped-only;
online reclaim uses a fresh verified snapshot rather than a live-file copy. Git GC is
never concurrent with compaction writes and retains Git's configured grace window.

CD-0009 active research cleanup is governed by its proof-backed archive operation and
PM7, not PM10 reclamation. Active pack tables are included in backups while present;
live deletion does not retroactively alter verified backups, which age out under the
ordinary retention policy.

## 4. Invariants

1. Only accepted SQLite and git authorities are required for clean-machine recovery.
2. Every promoted backup and restore passes SQLite triple verification; `integrity_check`
   alone is insufficient because it does not check foreign keys.
3. A recovery never serves a partial DB, mixed git/DB generation, or unrebuildable
   projection as complete.
4. Rebuilds are deterministic folds, not `.recover` output.
5. PM10 reclamation never deletes retained SQLite/git authority or asserts recovery
   for PM8/PM9-excluded data; CD-0009 archive separately deletes temporary active-
   research authority after durable-output proof.

## 5. Migration and acceptance proof

Schema manifests are versioned. Restore to a newer compatible binary runs explicit
upcasters then rebuilds; restore to an older unsupported binary rejects before serving.
The production gate is an automated clean-machine drill that restores only the manifest,
SQLite snapshot, and git clones; rebuilds every disposable projection; validates note
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
