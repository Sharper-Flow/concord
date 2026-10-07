# CD-0111: A release never requires a session restart

- **Status:** Accepted
- **Date:** 2026-09-05
- **Scope:** The adapter-to-core binding a running session holds, the installer's
  retention of release directories, and the moment a breaking store migration
  applies
- **Approval:** The operator approved this decision for issue #841 on work item
  `work-c128cbe6b07ea2d61c7d105a`.
- **Related:** CD-0005, CD-0027, CD-0096, CD-0105, issue #841, issue #722
- **Amended by:** [CD-0191](CD-0191-a-stale-session-sees-its-replaced-release.md) adds the lane-surface staleness rule beside D1 and D2. The adapter marks a session stale when the installed release differs from its pinned release. Every result then carries one bounded notice, the dispatch_worker action refuses before any core call, and the installer's retention output names the holder sessions. D1 and D2 stand unchanged.
- **Amends:** `docs/agent-tool-surface-evolution.md`, acceptance criterion 3

## Context

Concord releases on every merged pull request. Thirty-two releases shipped in
the three days before this decision. The installer moves the `concord` launcher
and the `current` root to the new release and deletes the prior release
directory in the same transaction.

The adapter is plugin code loaded once per OpenCode process. It carries its
release manifest digest as a compiled constant and resolves the core binary
through `PATH` on every call. After an install, an old adapter meets a new
core, and the core refuses every call with `manifest_mismatch`. That refusal is
correct under CD-0005. The recovery it names is not: the surface-evolution
document and issue #841 both send the operator to restart the session, which
loses the session context mid-item.

The store already admits an older binary through a schema compatibility floor
(issue #722). An additive migration recorded by a newer binary does not refuse
an older one. Only a breaking migration closes that door. Two of the last eight
schema-touching merges were breaking.

## Decision

### D1. A session keeps the pair it started with

The adapter invokes the core binary at its own release path, never through
`PATH`. The release path is a generated constant beside the manifest digest.
A session that started on release N runs every call against the core of
release N until the session ends.

The digest check stays exact and fails closed. It never admits a second digest.

### D2. The installer retains a release a live session references

The installer removes a release directory only when no live host session
references it. The host owns session liveness. The installer reads the live
session set from the host and refuses the removal while any session holds the
release. The removal is retried at the next install.

An installer with no host observation removes nothing but the release it
replaced in its own transaction, and only when that release is older than the
one before it.

### D3. A breaking migration applies only through an explicit upgrade

The core never applies a breaking migration when it opens the store. An
additive migration still applies at open, under the compatibility floor of
issue #722.

A breaking migration applies through `concord upgrade`. The command refuses
while any live session holds a release older than the migration. The refusal
names each session and the release it holds. The operator chooses when to run
it.

An unstamped development build is not a release. It applies no migration to an
existing store at open or upgrade. It may fully migrate a fresh store and may
operate a store that is already up to date. CD-0139 records this isolation rule.

Concord (CON) issue 807 sharpens D3's installation edge: an installer facing a pending breaking
step, or a store whose readiness it cannot establish by reading, prepares the
candidate release durably and does not activate it. The active launcher, the
current root, the tools and the agents stay on the release the running sessions
hold. Activation completes only after the operator runs the migration and the
activation commands the installer names. The core exposes this as a read-only
readiness plan that reads the store through one read-only SQLite connection.
The plan does not create a missing store, change permissions, apply migrations,
repair manifests, or write logical state. The library's own log and
shared-memory files may appear beside the store. An absent store is a known
fresh state, because it holds no shared state that a breaking migration could
change. Any other unreadable or unknown readiness fails closed.

An incompatible migration and its release cleanup run only inside an explicit
operator maintenance boundary. The migration command opens the boundary: it
excludes new session admission before its final lease check, and the installer's
activation holds the same exclusion through the swap and the cleanup, then
reopens admission. A lease observation under that exclusion is more than a
snapshot; without it, a referenced release is never deleted. Cleanup safety is
an admission-exclusion invariant, not an observation count: no re-read closes
the window behind it, so cleanup deletes only after establishing that every
installed release tree that could still admit a session honors the shared
exclusion, and a tree that cannot — legacy, unmarked, or an unreadable
enumeration — retains every candidate until the offline bootstrap removes it.
A session whose lease cannot classify the release it holds, or a live session
the exclusion cannot fence, fails the boundary closed: the command refuses and
names it. Admission refuses a session claim whose pinned core no longer exists,
so a claim that waited behind cleanup never lands on a removed release. An
installed tree that cannot honor the exclusion but holds no live
session is the operator's decision: the migration command names it and proceeds
only when the operator confirms that no session runs on it.

A staged release tree carries only the maintenance-fence capability its own
core reports through the side-effect-free version descriptor. The installer
never asserts a capability on a core's behalf. A core that cannot run or does
not identify as the staged release refuses staging. A core whose descriptor is
absent, malformed, or names an unsupported protocol installs unmarked, so an
incompatible migration later names its tree and waits for the operator's
confirmation. The boundary the migration
opens names its operation and the release root that opened it, and the two
together are the ownership proof: the activation adopts and closes only a
boundary that the prepare-migrate-activate path opened for its own candidate
path. A foreign, an
unattributed, or an ownerless boundary is retained for the operator's offline
bootstrap, never closed by position and never adopted because a prepared
record happens to exist.

One maintenance command runs at a time. The migration command and every
installer command share one maintenance lock, and a second command refuses at
once. A migration command decides whether to close its boundary from what it
alone committed, and an installer command recovers transactions and may close
the boundary, so an overlap could reopen admission early.

The shared lock has one identity in both languages. It is a non-blocking
flock on the data root directory itself, taken before any recovery or
staging. The root is created before it is opened, so a first-install
bootstrap holds the same lock an established root holds. The open refuses a
symlinked root without following it. After the flock, the holder compares the
open descriptor's device and inode with what the path names now, read
without following symlinks: a root removed or replaced before admission
releases its stale descriptor, and a bounded local sequence re-opens the
current path. The sequence never sleeps or waits on contention.

No cleanup removes the data root. A held directory flock cannot make a
later path-based removal conditional on the inode the holder once
observed: between any check and any removal another participant may take
the directory over, so an owner-tagged removal could delete a directory
the command never created. The acquisition creates a missing root only to
lock it, and the empty root, its necessary ancestors, a root another
participant created or recreated, and any state in them are retained
after a failed installation, a normal maintenance release, and an
uninstall. An uninstall removes the intended installed content only.
Retained disk space is the accepted cost, and retention is what keeps a
Concord cleanup from deleting a replacement holder's directory; exclusion
against an arbitrary external replacement of the root is not promised.

The recorded migration command pins the environment the plan read: it unsets
the inline database override and names the data home, so an operator shell
cannot point the migration at another store. While a boundary is open, the
prepared candidate's migration may have committed. An install whose plan is
blocked or unknown then refuses and keeps the prepared record and the boundary,
so the recorded activation still completes.

Schema-floor admission is necessary, not sufficient. The floor lets a binary
that defines it open a store that later additive steps have reached, which is
what makes compatible pairs coexist. It does not promise compatibility for
every release pair: a pair also needs the pinned adapter/core identity D1
records, and a breaking step beyond a binary's definitions still refuses it.

A store whose manifest cannot be read by looking is not repaired by the core or
by the installer. The operator owns an offline bootstrap: end every session,
stop the launcher, copy the database file aside, and rebuild the manifest with
a release binary that defines the store's schema, or restore the copy. The
readiness refusal names this route; no automated path guesses a manifest.

### D4. The typed refusal never asks for a restart

No core refusal and no adapter refusal names a session restart as its
recovery. `manifest_mismatch` remains the refusal for a digest the core does
not recognize. Under D1 it is reachable only through a defect, and its recovery
action is `contact_operator` with the two digests.

CD-0191 amends this rule for one refusal: the adapter's `dispatch_worker` gate
for a stale session names a session restart as its remedy, because the lane
text a dispatch would run was already replaced on disk by the install. Every
other refusal, and every non-dispatch operation in a stale session, keeps this
decision's guarantee unchanged.

## Acceptance Criteria

```gherkin
Scenario: a release installs while a session runs
  Given a session that started on release N
  When release N+1 installs on the same host
  And the session records a workflow action
  Then the core of release N accepts the action
  And the session is never told to restart

Scenario: the installer keeps a referenced release
  Given a live session that holds release N
  When the installer commits release N+1
  Then the release N directory remains on disk
  And the installer removes it at a later install once no session holds it

Scenario: a session never lands on a removed release
  Given a session claim waits while the installer removes release N
  When the claim is admitted after the removal
  Then the claim refuses and names the removed core
  And no lease records release N for that session

Scenario: a breaking migration waits for the operator
  Given release N+1 carries a breaking store migration
  And a live session holds release N
  When the installer installs release N+1
  Then the installer prepares release N+1 durably without activating it
  And the launcher, the current root, the tools and the agents keep release N
  And the installer names the pending breaking migration and the exact migration and activation commands
  When a session on release N+1 opens the store before the migration
  Then the store refuses to open and names the pending breaking migration
  When the operator runs the prepared candidate's migration command
  And a live session still holds release N
  Then the command refuses and names the session on release N
  When the operator ends that session and runs the migration command
  Then the migration applies inside the maintenance boundary
  And the operator re-runs the recorded activation command
  And the prepared release N+1 becomes the current release
  And a session that starts afterwards opens the store on release N+1

Scenario: the maintenance boundary excludes session admission
  Given an installer prepared release N+1 for a breaking migration
  When the operator runs the prepared candidate's migration command
  Then session admission is excluded before the final lease check
  And the exclusion is held through the migration and the activation
  And a session that starts inside the boundary fails closed
  And the refusal names the recorded activation command
  When the activation completes
  Then session admission reopens on release N+1
  And a referenced release is never deleted on a lease snapshot alone
  And the cleanup removes a release only under the held exclusion
  And the cleanup first proves every installed release tree honors the exclusion
  And a tree that cannot honor the exclusion retains every candidate

Scenario: the boundary belongs to the release that opened it
  Given an open maintenance boundary with no release attribution
  And a prepared record for a waiting candidate
  When the recorded activation command runs
  Then the activation refuses instead of adopting the boundary
  And the unattributed boundary remains exactly as written
  And a boundary that names another operation is refused the same way
  And the same refusal binds an install superseding the candidate
  And only the offline bootstrap closes an unattributed boundary

Scenario: two migration commands never overlap
  Given a migration command holds the maintenance boundary
  When a second migration command starts
  Then the second command refuses before it reads the store
  And the second command opens no boundary and commits nothing
  And an installer command started meanwhile refuses the same way
  When the first command commits the breaking migration
  Then the boundary stays open until the prepared release activates

Scenario: the maintenance lock keeps one identity in both languages
  Given a data root no command has created yet
  When the first command takes the shared maintenance lock
  Then the root exists and every other command refuses at once
  And the core's migration command and the installer's commands refuse the same way
  When the root is removed or replaced before a command is admitted
  Then the command releases its stale descriptor
  And a bounded local sequence re-opens the root the path now names
  And the sequence never sleeps or waits on contention
  When a command finishes without writing state
  Then the empty root it bootstrapped and its ancestors are retained
  And a root another participant created or recreated is never deleted
  And no transaction cleanup or mid-command step removes the data root
  When an uninstall finishes with an empty data root
  Then the intended installed content is gone and the lock root remains

Scenario: a blocked install keeps the open boundary's candidate
  Given an open maintenance boundary for a prepared release N+1
  When an install of release N+2 finds its own activation blocked
  Then the install refuses and names the recorded activation command
  And the prepared record and the boundary remain unchanged
  When the operator runs the recorded activation command
  Then release N+1 becomes the current release and the boundary closes

Scenario: an unknown participant fails the boundary closed
  Given an open maintenance boundary
  And a lease whose schema version cannot be read
  When the migration command runs its final lease check
  Then the command refuses and names the unreadable participant
  And an unknown or unfenceable legacy session blocks the boundary

Scenario: the operator confirms an unfenceable tree is idle
  Given an installed release tree that cannot honor the exclusion
  And no live session holds that tree
  When the migration command runs without the operator's confirmation
  Then the command refuses and names the tree and the confirmation
  When the operator confirms that no session runs on the tree
  Then the migration applies inside the maintenance boundary
  And the command names the tree it proceeded past
```

## Verification

- Adapter tests prove the core path is the generated release constant and
  that no call resolves `concord` through `PATH`. A refused admission under
  an open boundary carries the boundary's notice into the session.
- Installer tests prove retention under a live-session observation and
  bounded removal without one, prepare-then-activate with the recorded
  commands, the held exclusion across the swap and cleanup, and
  candidate-forward recovery from every crash phase after a committed
  breaking migration. They also prove the cleanup capability gate: a
  legacy admission at the instant of deletion retains its release, and
  cleanup resumes only after the offline bootstrap removes the unfenceable
  tree. They prove boundary ownership by attribution: an unattributed open
  boundary survives a prepared activation and a superseding install
  untouched, and recovery fixtures carry the attribution the migration
  command itself writes.
- Store tests prove open never applies a breaking migration and `concord
  upgrade` refuses under a live older session. Readiness tests prove the
  plan writes no logical state, reports the applied breaking floor, reads
  committed log state through a live cross-process index, and fails closed
  on unknown states, additive steps beyond the binary, and breaking steps
  beyond it. Host-lease tests prove admission refuses a claim whose pinned
  core was removed.
- Host-lease and installer tests prove the shared maintenance-lock identity
  in both languages: the first-install bootstrap creates and locks an absent
  root, a symlinked root is refused at the open, a root replaced between the
  open and the flock is re-acquired on the current inode, a root replaced by
  a symlink is never admitted, the lock dies with its holder, and the
  read-only plan takes no lock and creates no missing store. They prove the
  retention rule: the empty bootstrapped root, its ancestors, a
  foreign-created root, and a root replaced or recreated during a hold all
  survive a failed install, a crashed install and its recovery, a normal
  release, and an uninstall; no transaction cleanup removes the data root,
  and an uninstall removes the intended installed content only. The
  final-cleanup interleaving — a replacement published and held while the
  holder's cleanup runs — is covered deterministically and proves the
  release removes no data root at all. Both interop directions run the real
  core binary beside the real installer.
- The release-pair test proves one representative compatible pair with the
  actually released source: each release's own adapter claims its lease by
  calling its own pinned core, the old session keeps operating after the newer core
  advances the store, and the released tree reads honestly as unfenceable.
- The surface-evolution acceptance criterion names `contact_operator` as the
  mismatch recovery.
- `python3 scripts/check-doc-contract.py` and
  `python3 scripts/check-knowledge-index.py` pass.
