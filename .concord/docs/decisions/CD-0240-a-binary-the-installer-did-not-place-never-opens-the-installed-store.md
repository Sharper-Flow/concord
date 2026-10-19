# CD-0240: A binary the installer did not place never opens the installed store

- **Status:** Accepted
- **Date:** 2026-10-19
- **Scope:** Which binaries may open a store whose directory is a data root
  the installer manages
- **Amends:** [CD-0139](CD-0139-unstamped-build-store-isolation.md) D3, the
  up-to-date store an unstamped build may operate, and D5, the stamped
  release behavior it keeps
- **Preserves:** CD-0139 D1, D2, and D4; the repository-path refusal of the
  database override; [CD-0111](CD-0111-a-release-never-requires-a-session-restart.md)
  D3 migration behavior for the binaries the installer places
- **Approval:** 2026-10-19. Operator approval of this text for
  [Concord (CON) issue 992](https://linear.app/sharper-flow/issue/CON-992).

## Context

CD-0139 stops an unstamped build from migrating an existing store. Its D3 still
lets that build operate any store whose migrations it already knows, and no
rule checks a stamped build at all. Every host shell carries `CONCORD_DB_PATH`
naming the installed store, so a binary an agent builds from a branch, or runs
as a test, opens that store before its command body runs. A host instruction
that set a synthetic path per session was the only guard, and it is retired.

The version stamp cannot tell an installed release from a test build: the
release-pair and black-box tests stamp their builds with release versions. The
location can. The installer marks a data root it manages with the `current`
entry, and every binary it runs lives under that root: an installed release
tree, a prepared candidate, and a transaction stage.

## Decision

### D1. Only a binary under a managed data root opens its store

A data root is managed when it holds a `current` entry of any kind. A binary
opens, migrates, plans, backs up, verifies, restores into, or recovers a store
whose directory is a managed data root only when its resolved executable path
lies under that root. Any other binary refuses, whatever its version stamp,
before it touches the store or the root's leases, locks, and fences. A data
root or executable that cannot be resolved refuses.

### D2. The refusal names the installed release and the isolation route

The refusal names the running binary, the data root, and the release that
`current` names. Its recovery is to run the installed release, or to set
`CONCORD_DB_PATH` to a fresh path outside any repository and restore a backup
there to work against the same history.

### D3. CD-0139 applies outside a managed data root

This amends CD-0139 D3: an unstamped build operates an up-to-date existing
store only when D1 admits it. It amends CD-0139 D5: a stamped build keeps its
migration behavior for a managed store only when D1 admits it. CD-0139 D1, D2,
and D4 stand. No unstamped build reaches a managed store from outside its
root, so the unstamped migration refusal names no owning release.

## Alternatives considered

- Refuse only unstamped builds. Rejected: test builds stamp release versions.
- Admit a stamp that equals the release `current` names. Rejected: a prepared
  candidate runs its upgrade before activation moves `current`, and a test
  build can carry any stamp.
- Refuse only writes. Rejected: opening a store in write-ahead-log mode writes
  beside it, and a test build has no reason to read live state.
- Keep the host instruction. Rejected: an instruction is not a mechanism.

## Consequences

A developer who needs the installed history restores a backup of it to a fresh
path and points the build there. A release binary run from outside the data
root, such as a downloaded archive, refuses the installed store until the
installer places it. The owner and live-session lines of the unstamped
migration refusal are removed, because no admitted route reaches them.

## Verification

- `internal/store.TestAdmitStorePathAdmitsOnlyBinariesUnderAManagedDataRoot`
  admits an installed tree, a symlinked launcher, a transaction stage, an
  aliased data root, and an unmanaged store, and refuses a binary outside the
  root, a sibling whose name extends it, a relative path, and an unlocatable
  binary.
- `internal/store.TestAdmitStorePathFailsClosedOnAnUnexplainedCurrentEntry`
  refuses a regular file and a dangling link.
- `internal/store.TestReleaseManagedStoreRefusesEveryPathEntryPoint` refuses
  every store entry point that takes a path, for an unstamped and a stamped
  build, and leaves the data root byte-for-byte unchanged.
- `cmd/concord.TestReleaseManagedStoreRefusesABinaryTheInstallerDidNotPlace`
  refuses each command route, names the installed release and the recovery,
  and leaves the data root, its leases, locks, and fences unchanged.
- `cmd/concord.TestReleaseManagedRestoreDestinationRefuses` refuses a restore
  into a managed data root.
