# CD-0212: Reclaim gates the live head and records retained refs

- **Status:** Accepted
- **Date:** 2026-10-08
- **Scope:** The worktree reclaim, destroy, and audit content gates; the
  separation of directory-removal proof from stored-ref deletion; the bounded
  native-removal plan, its original-plan replay, and its durable per-ref
  outcomes; the agent payload schemas that carry these facts,
  [Concord (CON) issue 829](https://linear.app/sharper-flow/issue/CON-829)
- **Amends:** CD-0096 D3 Destroy at the gate subject, and CD-0144 D1-D2 at
  the branch each content class reads
- **Preserves:** CD-0181 in full, CD-0195 D2 in full, the clean-tree,
  observed-session, unstarted, unpublished-lesson, and operator-approval
  gates, and claim-time branch and base identity
- **Related:** CD-0095, CD-0096, CD-0105, CD-0118, CD-0144, CD-0178,
  CD-0179, CD-0181, CD-0195
- **Approval:** The operator approved contract version 1 of work item
  `work-6cc7450c84e7077407a4f7b5` on 2026-10-08 — recorded by
  `approve_contract` at work version 14, operation
  `workflow-740de6d41fb4a8114c3d732c` — and this record is legislated under
  that contract (CD-0159 D2-D3). This citation records contract approval,
  not verbatim approval of every later exact commitment.

## Context

The reclaim probe read the live worktree's branch and discarded it. Every
content gate then ran against `entry.Branch`, the branch the claim recorded.
The audit classifiers and the unpublished-lesson classifier read the same
stored column. Nothing compared the two.

A checkout that moved to a correction branch therefore gated on a branch it
no longer held. The reported shape was terminal: round-1 commits stayed on
the stored claim branch unmerged, the correction round moved the checkout
onto a new branch, and the squash merge deleted that branch's remote head.
The stored branch failed the durable count forever, so a clean terminal
worktree with fully merged content refused reclaim without a destructive
approval no safety question justified.

The inverse shape was worse. A durable stored branch admitted a reclaim whose
live `HEAD` sat on an unproven branch, or on no branch at all. Removing a
detached checkout can drop commits no ref protects. The probe also planned
`branch -D` for the stored branch without proving or pinning it, and a
removal that deleted the directory but failed the branch deletion lost the
debt on retry, because the converged probe answered with no removal at all.

Independent review found five replay defects in the first repair. A retry
repinned the live `HEAD`, so a detached checkout holding new unique content
was removed. A pinned deletion dereferenced a symbolic ref and deleted its
foreign target. A merge target spelled `refs/heads/main` bypassed the default
guard, and the guard trusted the caller instead of the repository. A forced
removal lost its recorded force on retry. A changed-tip protection vanished
from a fresh audit, because the committed facts still carried the ref as a
pending deletion.

A second independent review found four boundary defects in the second
repair's ref deletion. The default ownership was not re-derived at the
deletion, so a default that moved onto a planned branch after the plan
committed was deleted. An empty protected set nothing established counted
as established, so a repository with no caller default, no `origin/HEAD`,
and a detached `HEAD` deleted its main branch. The checkout guard ran only
before the pinned transaction, which enforces no checkout guard of its own,
so a worktree gaining the branch at the boundary kept an unborn `HEAD`. And
the fold's branch column carried the claim-branch bound, so a valid long
live branch failed the reclamation fold instead of being retained. D3 and
D4 below legislate the repaired boundary.

A third review — the coordinator's rejection of the third repair — found
four more defects in the native phase that survived those repairs. A failed
post-deletion checkout observation, and a failed restoration of a raced
ref, each erased their recovery debt on the next replay, because a missing
ref settled the deletion before any checkout inspection and no durable
phase distinguished restoration debt from deletion debt. A malformed but
successful worktree inventory authorized deletion, because the checkout
probe matched text lines instead of validating complete entries and
observing every listed `HEAD`. And the observed-identity bound the repair
pinned to `PATH_MAX` was wrong: a reftable-backed repository holds valid
branch identities no loose pathname could, so a 5011-byte live branch
failed the reclamation fold. D3 and D4 carry these repairs.

A fourth review — the salvage review of the interrupted repair — found
four admission defects in the durable settlement owner and the checkout
observation. A settlement event recorded by an earlier invocation could
authorize a phase move against a different current predecessor, because
the event identity named only the target phase. A replay executed the
pinned deletion while the durable row still said `restored`, and a second
cycle's transitions collided with the first cycle's events. The row then
mutated under an old event, and a log rebuild refused on an order the live
row never followed. The post-deletion observation read the inventory's
branch lines but never each listed `HEAD`'s live symbolic identity, so a
checkout that gained the branch after the list was produced settled as
absent. The inventory parser accepted records without their blank
separators or terminating boundary, repeated attributes, and repeated
worktree paths. A pre-deletion observation that could not run left the row
silently pending, still carrying its original durability reason. D4
carries these repairs.

## Decision

### D1. One immutable live `HEAD` observation is the content authority

Every reclaim, destroy, and audit gate reads one observation captured before
any effect: the branch the live worktree has checked out, or the detached
marker, the tip SHA, and the clean-tree fact. The direct reclaim, the audit
classification, the audit reclaim pass, the non-destructive destroy, the
unstarted count, and the unpublished-lesson comparison all consume that
observation.

The gates run against the observation's revision: the live branch, or the
detached tip when no branch is checked out. The claim keeps its branch and
base identity unchanged. A durable stored branch is not proof that a
different or detached live `HEAD` is durable, so the stored column alone
proves nothing about the content a removal would lose.

CD-0181 passes exactly as before, now applied to that revision: remote
reachability first, then containment by one default-ref commit with an equal
`git patch-id --stable`. No cumulative squash, no network service, and no
GitHub authority enters the comparison.

### D2. The refused edges keep their boundaries

An unproven live `HEAD` refuses, and the refusal names the live branch or the
detached tip it measured. Dirty content, live occupancy, unpublished live
lessons, unreachable required probes, and non-terminal destroy keep their
existing refusals. A destructive removal still requires the operator
approval it consumes, and that approval skips the content gates only for the
named removal.

### D3. Directory-removal proof is separate from stored-ref deletion

Proving the live checkout durable proves the directory removable. It does
not prove any ref deletable. A stored claim ref is a deletion candidate only
under its own proof: when the live head is that branch, the live proof
covers it, otherwise the ref is proven independently and pinned to its
observed tip.

The default ref is never deleted, whatever the claim row says, and that
ownership guard runs before every deletion candidate, including the
checked-out claim branch itself. The guard establishes the protected default
independently of the caller's merge target: the normalized caller ref, the
repository's `origin/HEAD` target, and the repository's own checked-out
`HEAD` each contribute a short name, and accepted spellings normalize before
comparison. Absence is not establishment: a set no observation contributed
to — no caller ref, a missing `origin/HEAD`, a detached repository `HEAD` —
is unestablished, not empty-and-known, and when the set cannot be
established deletion permission fails closed: candidate refs are retained
with the reason recorded, and a proven-durable live removal is never blocked
by the unknown.

A live branch that diverged from the claim is retained, because no authority
on this path owns its deletion. A stored ref whose content is not proven
durable survives, and a stored ref that became symbolic survives the same
way, because deleting through it would delete its target. Each retention is
recorded in the reclamation facts. A retention never blocks a proven-durable
live removal. A retained ref names a branch Git itself accepted, so the
retention projections and payloads carry no upper bound beyond
non-emptiness: loose refs admit names whose ref path fits `PATH_MAX`, while
a reftable-backed repository admits identities of any length its writer's
block size allows, so a fixed bound would narrow valid native identities.
The full retained identity is recorded without truncation, and the
claim-branch bound stays on the claim surface alone.

Every retained ref, every deletion the plan owes, and every protection the
native run produces also hold one durable outcome row, keyed by the claim
generation. The reclamation fold inserts the rows; a settlement fold behind
the native run retires an owed deletion or records a protection. The rows
survive directory removal and later claims, because the entry row a later
claim replaces cannot erase another generation's record. A fresh audit read
reports each unsettled row as its own `retained_ref` drift row naming the
branch, the tip, and the reason, with inspection as the only named action.
The audit reclaim row reports the retentions the reclamation recorded.

### D4. The bounded native-removal plan is recorded and honored

The reclamation event records the plan: the live head, its tip, the retained
refs, the consumed force with its approval reference, and the tip-pinned
branch deletions. The forced facts name the operator approval the committing
request consumed, composed at the append boundary, so a probe captured
before the approval tail still records the reference that was consumed. The removal runs after the commit with no transaction
open, as CD-0195 D2 requires, and revalidates native identity before every
destructive effect: the checkout must still belong to the probed repository
at the recorded tip, and a branch is deleted only when it is still the
direct ref the plan pinned, still at its pinned tip, not the repository's
default as the repository stands now, and the complete pre-deletion
observations report no holder among the listed worktrees. This observed
absence does not atomically exclude concurrent checkouts. Ownership is
re-derived before deletion, never trusted from the plan's earlier
observations: a default that moved onto a planned branch after the plan
committed retains it, an unestablishable default set fails closed exactly as
the plan-time guard does, and a ref whose tip changed is retained with the
protection naming the ref.

The pinned deletion is one pinned argv git command with `--no-deref` —
`update-ref -d <ref> <expected-tip>` — so the old-value check and the
mutation run inside a single git process, and the named ref itself is the
subject. No wrapper, hook, or concurrent actor can interpose between the
old-value check and the deletion the way a name-addressed `branch -D`
invites. This check does not exclude concurrent checkout changes. A ref that
became symbolic can never redirect the deletion onto its target.
Refs resolve by their exact `refs/heads` path, so a tag that shadows a
branch name cannot bend the pin. `update-ref` enforces no checkout guard of
its own. The native protocol therefore observes the
complete porcelain worktree inventory — every entry validated whole, with
the symbolic identity of every listed `HEAD` read live — before the
deletion and again after it. These reads are not atomic with checkout
changes. Absence applies to the points when the listed `HEAD`s were
observed, not to the interval between the reads or to later checkouts.
Validation is structural: records are
separated and terminated by truly empty boundary lines — a whitespace-only
line is content, not a boundary — every attribute a complete record
carries once, and a worktree path appears in exactly one record. Holders
accumulate instead of ending the observation at the first one, so a failed
later observation is never hidden by an earlier holder. An inventory that
cannot run, or a successful inventory that is malformed or incomplete, is
an unknown observation: a failed pre-deletion observation refuses deletion,
and a failed post-deletion observation leaves recovery debt. When the
post-deletion observation finds a listed `HEAD` naming the deleted branch,
it records restoration debt — an unborn `HEAD` still names its branch —
and restoration uses a create-only argv update at exactly the
pinned tip the plan recorded, whose zero old-value refuses to overwrite
anything a concurrent actor rebuilt. A successful update restores the
checkout at the pinned tip. Failed observation or restoration leaves durable,
audit-visible recovery debt, not proof of successful restoration. A foreign
rebuilt ref remains unchanged. The independent stored-ref proof
also runs against the tip the plan pins, never against the mutable branch
name.

An absent ref settles nothing by itself. A pinned deletion that ran, or may
have run on an earlier attempt, owes recovery until complete inventory
observations report no listed worktree holding the branch at the observed
points. Only that observed absence converges
the debt, while a checkout that still names the absent branch owes its
restoration at the immutable pinned tip. A pre-deletion observation that
cannot run or complete cannot exclude a holder, so it records the same
recovery debt with the failed-observation reason — never a silent pending
row that still carries its original durability reason. A post-deletion
observation or restoration that cannot complete records durable recovery
debt — visible to
the audit like every other unsettled outcome — and a later replay resolves
it under the recorded plan instead of treating the unobserved absence as
completion. Verified absence converges; unobserved absence never does.

One durable plan drives the first native execution and every replay. A retry
decodes the plan from the committed facts and never re-derives its
authority: it never repins the live `HEAD`, never rebuilds force or deletion
permission from the retry's own arguments, and revalidates against the
recorded identity. A surviving directory whose content changed after the
plan refuses instead of being removed under a fresh tip. A retry of an
approved forced removal keeps its recorded force, and a retry declaring
destructive authority over a safe plan stays unforced.

Identity protections are sticky: a ref that moved from its pinned tip, or
became symbolic, keeps its protection on every later replay, even when the
tip returns to the pinned value. The checkout and default protections are
re-derived each replay, because they track live conditions git itself owns.
A protected ref never masks an independently safe pending directory or
another debt: the retry converges what it can and reports the protection
beside it.

The durable owner of a ref's state is one recorded phase, not an inference
from git shape. Each row of the reclamation's per-ref outcomes holds one of
seven phases — `planned`, `checkout_proven_absent`, `deleted`,
`restoration_owed`, `restored`, `retained_unproven`, `settled` — and a
typed transition table is the only thing that moves it: the ownership
observation re-derives authorization from every pending phase and records
the absent-ref verdict directly, the pinned deletion starts only from a
persisted `checkout_proven_absent`, the post-deletion observation starts
only from `deleted`, and the create-only restoration starts only from
`restoration_owed`. `retained_unproven` and `settled` admit no step:
identity protection and completion are terminal, while a `restored` ref
stays replayable because its protection tracks a live checkout. The
projection row is fold state of the settlement log. The settlement owner
refuses any move the table does not admit and keeps a refused row at its
recorded phase. A settlement the row already holds — the same phase, tip,
and reason — converges with no new event and no mutation at all. Any other
settlement is a transition, and a transition is authorized only by a fresh
chronological settlement event appended for exactly this invocation. Its
identity derives from the durable predecessor and the per-ref count of
settlement events already logged, never from this invocation's incidental
caller or timing. A historical event recording the target phase therefore
never authorizes a phase reentry, and event equivalence is never the
authorization. A stored event under the computed identity is the same
transition already logged: landing its fold runs the same admission, and a
different payload under the identity refuses. The projection rows rebuild
from that log alone, so a log rebuild replays the same trajectory the live
row followed, restoration debt included. Admission precedes the fold scope
and any append failure rolls the whole transaction back, so a settlement
can never commit an unclosed fold guard; the authorization a native effect
needs is persisted before the effect runs, and the phase survives
directory removal, interruption, replay, later claims, and audit.

The native run yields one bounded outcome per ref — settled, protected
with its observed tip and reason, or recovery owed at the pinned tip — and
an idempotent SQL-only settlement persists those outcomes behind the run,
keyed by the claim incarnation, with no work-item version advance. A
deletion the plan owes survives the directory removal, and a replay of an
already-recorded outcome converges. The store transaction stays SQL-only,
and the native removal stays behind the commit.

## Alternatives considered

- **Gate on the stored branch and re-pin the claim to the live head.**
  Rejected: it rewrites claim-time identity, which the claim surface owns,
  and hides the drift instead of diagnosing it.
- **Refuse every checkout that drifted from its claim.** Rejected: the
  reported terminal worktree never reclaims, and the refusal names a branch
  the worktree does not hold.
- **Delete the divergent live branch when its content is durable.** Rejected:
  durability says the content survives, not that this path owns the ref.
- **Ask the hosting service whether the pull request merged.** Rejected:
  CD-0144 D4 keeps the audit local, and CD-0181 already answers locally.

## Consequences

- A clean terminal worktree on a squash-contained correction branch reclaims
  without destructive approval once the remote branch is deleted.
- An unproven live `HEAD`, including a detached tip outside every ref, refuses
  and names the head it measured.
- Unproven stored claim refs accumulate as visible retained refs instead of
  blocking removals or being deleted unproven.
- Every audit read reports each unsettled ref outcome as its own
  `retained_ref` drift row, so the operator who arrives after the directory
  is gone, or after a later claim, sees the branch that survived and why.
- Branch deletions run as pinned no-deref argv git commands, so a
  runner-level wrapper can no longer move a ref between the pin and the
  mutation, and a symbolic ref can no longer redirect a deletion onto its
  target. The whole native phase is carried by argv primitives.
- A replayed removal keeps the recorded plan's force and identity: retries
  converge approved forced removals, refuse changed surviving content, and
  never escalate a safe plan.
- Moved and symbolic protections are durable and sticky; the operator
  disposes of those refs by hand, and a returning tip does not reopen the
  deletion. Checkout and default protections are re-derived at every
  boundary, so a retried removal converges the pinned deletion once the
  other worktree moves off the branch or the default moves away.
- The pre-deletion observations do not atomically exclude concurrent
  checkouts. A post-deletion observation that finds a listed holder of the
  deleted branch records restoration debt. When the create-only restoration
  succeeds, the second
  worktree's `HEAD` resolves to the pinned tip. Failed observation or
  restoration leaves durable, audit-visible recovery debt. A concurrently
  rebuilt ref remains unchanged, and the recorded outcome is reported beside
  whatever else the removal converged.
- A pre-deletion observation, a post-deletion observation, or a restoration
  that cannot complete leaves durable, audit-visible recovery debt at the
  immutable pinned tip. A healthy replay resolves it — restoring the ref a
  checkout still names — and never settles an absence it could not verify.
- The durable outcome rows are fold state of the settlement log:
  transitions append fresh chronological settlement events, and a rebuild
  from the log alone reproduces the exact rows, restoration debt included.
- A malformed or incomplete successful worktree inventory refuses the
  deletion it was asked to prove, because an unknown checkout state is
  never proof of holder absence at the observed points.
- A valid long live branch — any identity Git itself accepted, including a
  reftable identity no loose pathname could hold — reclaims without failing
  the reclamation fold, and its full identity stays recorded and visible to
  the audit.
- The audit content classes report the live head, so a drifted checkout is
  diagnosed by the branch that actually prevents removal.
- The agent payload schemas gain `head_branch`, `head_detached`, and
  `retained_refs` for these facts, and their generated projections carry the
  extension.

## Verification

- `internal/store.TestCorrectionCheckoutReclaimsAcrossAllPaths` asserts the
  correction checkout reclaims across the direct reclaim, the audit
  classification, the audit reclaim pass, and the non-destructive destroy,
  and that the unstarted, unpublished-lesson, dirty, and occupied edges keep
  their refusals.
- `internal/store.TestReclaimProtectsUnprovenLiveHead` asserts an unsafe live
  branch and an unproven detached tip refuse while the stored branch is
  durable, and that a detached tip the default ref holds reclaims.
- `internal/store.TestReclaimRetainsAndReportsUnprovenStoredRef` asserts the
  unproven stored ref survives a durable live removal with branch, tip, and
  reason recorded and reported, that a missing stored ref neither blocks
  nor owes, and that the retention stays visible on a fresh audit read.
- `internal/store.TestReclaimNativePlanProtectsChangedTipsAndConverges`
  asserts changed-tip protection, the never-deleted default ref, the
  convergence of a pending deletion after directory removal, that a ref
  moved at the deletion boundary itself is refused by the pinned
  transaction, that a branch checked out in another worktree is never
  deleted, that a failed directory phase retries under the recorded plan
  with no new event, and that committed forced facts name the consumed
  operator approval.
- `internal/store.TestReclaimReplaysRecordedRemovalPlan` asserts the
  original-plan replay and the durable outcomes: a changed detached or
  branched surviving directory refuses, a symbolic claim ref cannot delete
  a foreign target, the default ref stays protected under every spelling
  and when ownership cannot be established, an ambiguous tag cannot bend
  the pin, an approved forced removal replays and converges while a safe
  plan cannot be escalated, changed-tip debt and its settled protection
  stay visible on a fresh audit, a protected ref does not mask a pending
  directory, a moved-away-and-back tip stays protected, and a retention
  stays visible after a later claim.
- `internal/store.TestReclaimRevalidatesDefaultOwnershipAtNativeDeletion`
  asserts the boundary re-derivation: a default that moved onto a planned
  branch after the plan committed is retained with the directory phase
  unblocked, the protection stays visible to the audit, and a replay
  converges the pinned deletion once the default moves away.
- `internal/store.TestReclaimFailsClosedWhenNoDefaultIsEstablished` asserts
  that an empty set no observation established fails closed: even a
  consumed operator approval retains the unestablished default while the
  approved directory removal proceeds and the retention stays visible.
- `internal/store.TestReclaimProtectsCheckoutRaceAtDeletionBoundary` asserts
  that the exercised checkout race is detected after the
  pinned transaction, restored at its pinned tip, reported as a visible
  protection, and converged by a replay once the other worktree moves off.
- `internal/store.TestReclaimRecordsLongLiveBranchRetention` asserts that a
  durable checkout holding a valid long live branch reclaims, and the full
  retained identity stays recorded in the reclamation facts and visible to
  the audit.
- `internal/store.TestReclaimPostDeleteRecoveryDebtPersistsAcrossReplay`
  asserts that a failed post-deletion checkout observation and a failed
  create-only restoration each leave durable recovery debt a healthy replay
  resolves by restoring the ref at its immutable pinned tip for the worktree
  that holds it, with the settled protection visible to the audit.
- `internal/store.TestReclaimIncompleteInventoryNeverAuthorizesDeletion`
  asserts that a malformed successful worktree inventory never authorizes
  the pinned deletion, keeps a durable unproven outcome, and converges on a
  healthy replay.
- `internal/store.TestWorktreeInventoryParsingIsCompleteAndSymbolic` drives
  the inventory parser table: complete mixed inventories observe every
  listed `HEAD`'s symbolic identity, and every malformed or incomplete
  shape refuses.
- `internal/store.TestReclaimNativePhaseSurvivesInterruptionMatrix` drives
  the interruption and concurrent-restoration matrix across the native
  phase: an unavailable pre-deletion inventory, a failed pinned delete, a
  concurrently rebuilt ref preserved at its foreign tip, and the pinned argv
  primitives carrying the whole protocol under a stdin-refusing runner.
- `internal/store.TestReclaimValidReftableIdentityIsNotNarrowed` asserts
  that valid reftable live identities of 5000 and 6000 bytes reclaim, are
  retained whole, and stay audit-visible.
- `internal/store.TestReclaimRefusalReasonMatrix` drives the table-derived
  refusal matrix: dirty content, unproven live and detached heads,
  unpublished lessons, live occupancy, non-terminal destroy, destructive
  removal without approval, started work outside the unstarted gate, no
  active worktree, an unresolvable default ref, an unreachable status
  probe, a surviving directory that changed after the recorded plan, and
  unparsable recorded facts each refuse typed with the worktree kept.
- `internal/store.TestWorktreeRefPhaseMatrixCoversAllOrderedPairs` walks
  all 49 ordered pairs of the seven phases through the durable settlement
  fold: a pair the typed transition table admits — or a same-phase
  re-record — moves the row, every other pair refuses typed and leaves the
  row unchanged, and each refused pair carries its enumerated structural
  refusal reason while the five live-state reasons stay bound to the
  behavioral boundary tests.
- `internal/store.TestWorktreeRefPhaseReplayResumesRecordedPredecessor`
  asserts the replay entry: the terminal phases retire their deletions,
  every pending phase — including a restored ref whose checkout protection
  is re-derivable — resumes with its recorded predecessor.
- `internal/store.TestReclaimPreReadHolderRefusesDeletionAndKeepsPhase`
  asserts the holder refusal through the native boundary: a pre-read
  holder keeps the row at its recorded pending phase with the refusal
  reason settled over it, and a replay converges once the holder releases
  the ref.
- `internal/store.TestReclaimSettlementCommitsNoStrandedFoldGuard` ports
  the coordinator's replay diagnosis: an interrupted native run that
  already recorded a phase inline settles the same outcome again without
  committing an unclosed fold guard, and the healthy replay still resolves
  the recorded restoration debt at the pinned tip.
- `internal/store.TestReclaimReplayRecordsAuthorizationBeforeNativeDeletion`
  asserts that a replay of a reclamation a first attempt restored
  re-records the `checkout_proven_absent` authorization — moving the
  durable row — before the pinned deletion runs, and converges to
  `settled` only through that admission.
- `internal/store.TestReclaimDelayedPreparedReplayCannotDeleteStickyProtectedRef`
  asserts that a replay prepared under a stale phase, then committed after
  a first attempt recorded `retained_unproven`, reads neither the stale
  phase nor a historical settlement event as permission. The sticky
  protection holds after the tip returns to the pinned value.
- `internal/store.TestReclaimReadsListedSymbolicHeadsAroundNativeDeletion`
  asserts that a listed checkout whose `HEAD` comes to name the deleted
  branch after the inventory was produced is observed live, leaving
  restoration debt and a create-only restore instead of a settled report
  over an unborn checkout.
- `internal/store.TestWorktreeInventoryParserRefusesIncompleteRecords`
  asserts that the parser refuses records without their blank separators
  or terminating boundary, a missing or repeated attribute, a bare record
  claiming a `HEAD`, a repeated worktree path, and a whitespace-only line
  standing in for a boundary.
- `internal/store.TestReclaimFailedPreObservationLeavesDurableUncertainty`
  asserts that an unavailable pre-deletion observation leaves a durable
  recovery phase carrying the actual failed-observation reason, visible to
  a fresh audit after the safe directory removal.
- `internal/store.TestReclaimRefOutcomeRowsSurviveLogRebuild` asserts that
  every phase move of a second replay cycle is a fresh chronological
  settlement event, so rebuilding the projections from the log alone
  reproduces the exact durable rows — phase, tip, and reason.
- `internal/store.TestReclaimInterruptedRestorationDebtSurvivesLogRebuild`
  asserts that restoration debt left by an interrupted repeat cycle is a
  logged transition the log rebuild retains, and a healthy replay
  afterwards still restores the ref a checkout names at the pinned tip.
- `internal/store.TestReclaimObservesEveryListedHeadBeforeConcluding`
  asserts that the pre-deletion observation reads every listed worktree's
  symbolic `HEAD`, accumulating holders instead of returning at the first
  one, so a failed later observation surfaces as unknown uncertainty
  rather than a holder refusal.
- `python3 scripts/generate-agent-contracts.py --check` verifies the
  regenerated payload schema projection.
- `python3 scripts/check-doc-contract.py`,
  `python3 scripts/check-knowledge-index.py`,
  `python3 scripts/check-knowledge-closure.py`,
  `python3 scripts/check-cd-allocation.py`, and
  `python3 scripts/check-doc-links.py` pass over this record.
