# CD-0139: Unstamped builds isolate store migration

- **Status:** Accepted
- **Date:** 2026-09-12
- **Scope:** Schema migration behavior for an unstamped development build
- **Approval:** The operator approved this decision for the development-store isolation fix.
- **Related:** CD-0002, CD-0082, CD-0111
- **Preserves:** Stamped release migration behavior and the existing repository-path refusal

## Context

An unstamped development build uses `internal/version.Value == "dev"`. It can
contain schema migrations that a released binary does not define.

The default database path is the durable store shared by every binary. An
unstamped build that opens that store can apply migrations and make the store
unusable by the installed release. The 2026-09-02 outage demonstrated this
failure mode.

## Decision

### D1. An unstamped build does not migrate an existing store

An unstamped build applies no additive migration at `Open` and no breaking
migration at `Upgrade` when the schema manifest already contains an applied
migration. The refusal writes no schema state and is safe to retry.

### D2. An unstamped build may create a fresh store

An unstamped build may create the schema manifest and apply all known migrations
when the manifest contains no applied migration. This is the isolated
development-store route.

### D3. An up-to-date existing store remains usable

An unstamped build may operate an existing store when every migration known to
the build is already applied. It does not run a migration pass in that case.

### D4. The refusal names the isolation route

The refusal names `CONCORD_DB_PATH` with a path outside any repository. The
repository-path refusal itself remains unchanged.

### D5. Stamped releases retain current behavior

A stamped release keeps the migration behavior defined by CD-0111 D3. It
continues to apply additive migrations at open and breaking migrations through
`concord upgrade`.

## Verification

- Store tests prove an unstamped open refuses an existing store without changing its manifest.
- Store tests prove an unstamped upgrade refuses an existing store without changing its manifest.
- Store tests prove an unstamped build migrates a fresh store and admits an up-to-date store.
- Store tests prove stamped open and upgrade behavior remains unchanged.
