# CD-0036: Breaking law supersession marks dependent contracts stale

- **Status:** Accepted
- **Amended:** 2026-10-18.
- **Date:** 2026-08-17
- **Scope:** Product-law evolution; workflow contracts; stale-work refusal
- **Approval:** Operator approval for CD-0036
- **Related:** CD-0006 D5/D10/R3, CD-0012, CD-0013, CD-0015,
  CD-0016, CD-0027, CD-0033

**Approval amendment:** [CD-0243 D6](CD-0243-four-roles-and-six-operator-approval-classes.md#d6-existing-approval-requirements-follow-these-classes)
governs D2: the choice between amendment and replacement requires approval only under its six classes. Revision identity and cutover checks remain required.

## Context

CD-0015 binds workflow contracts to accepted law IDs but does not record the
exact law contents used at planning. Its checks run at contract approval and
completion. A same-ID edit is therefore indistinguishable from the contents the
operator approved, while a workflow whose law is superseded can continue making
authoritative progress until completion.

That is unsafe for a breaking replacement. A long-running change may replace a
law and require every old consumer to stop. Concord must enforce that cutover in
workflow authority without claiming it killed or controlled the host process.

## Decision

Routine contract supersession under CD-0013 D11 does not require a consent prompt and does not enact a law revision. Actual law changes follow CD-0230 D4; breaking-cutover authorization, digest staleness, migration fencing, and compatibility floors remain separate obligations. [CD-0242 D7](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d7-a-revision-invalidates-only-what-depended-on-it) and [CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source) govern: a contract pins the law revisions its approval consumed, and no historical pins are retained.


### D1. Law revision identity

[CD-0242 D7](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d7-a-revision-invalidates-only-what-depended-on-it) and [CD-0242 D10](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d10-knowledge-is-an-input-and-a-law-edit-changes-only-what-it-changes) govern: contract approval pins exactly one content digest for each law ID in the contract's `spec_mandate`. A compatible amendment's new digest triggers reassessment only of the obligations whose meaning it changes, and a breaking supersession of a pinned revision makes the contract stale. Legacy contracts remain readable: they carry law IDs without revision pins and follow the amendment and replacement identity rules in D2.

### D2. Amendment and replacement have different identities

[CD-0242 D10](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d10-knowledge-is-an-input-and-a-law-edit-changes-only-what-it-changes) governs: a compatible amendment keeps the law ID and states the current rule in place, without amendment dates, override instructions, or change history, and a law edit triggers reassessment only of the obligations whose meaning it changes. A new independent decision receives its own record. A change that requires existing consumers to reconsider their contract is not a compatible amendment: it publishes a new law ID, marks the old record `superseded`, and declares the existing `supersedes` relation, so every law supersession is a breaking cutover. The operator makes this legislative choice through the Git delta; agents may recommend a path but do not infer compatibility as workflow authority.

### D3. Staleness scoped to dependents

[CD-0242 D7](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d7-a-revision-invalidates-only-what-depended-on-it) and [CD-0242 D10](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d10-knowledge-is-an-input-and-a-law-edit-changes-only-what-it-changes) govern: a revision invalidates only records that depended on its changed digests, and a law edit triggers reassessment only of the obligations whose meaning it changed. A breaking supersession therefore marks stale exactly the active contracts that consumed the superseded revision; no grandfather path exists for them.

The stale check runs inside the transaction that owns each authoritative
workflow action. It blocks new execution claims, checkpoints, evidence binding,
worker-result acceptance, verdicts, premise confirmation, and completion.
Read-only inspection remains available.

The closed recovery paths are:

1. use the operator-approved `supersede_contract` action to atomically supply
   and install a fully validated successor contract pinned to the accepted
   successor law; or
2. cancel or supersede the work item.

The replacing workflow receives no general bypass. Its approved `law_modifies`
mandate authorizes the Git delta before cutover. After cutover it uses the same
contract-recovery path as every other consumer before binding final evidence and
completing. This preserves CD-0006 D10 without creating a privileged writer.

### D4. Authority stop, not process preemption

Concord does not kill OpenCode sessions or arbitrary host processes. Raw worker
or host execution evidence may record what actually happened after cutover, but
the stale workflow cannot accept that result into Product truth. A typed lane
dispatch using Concord authority is refused before spawn when its active
contract is stale.

### D5. Linearizable boundary and deterministic history

Live contract approval validates current Git-derived laws and captures their revision pins as current rows in the same SQLite transaction; [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: no approval event, folding, or replay exists.

Every later mutating preflight compares the active contract with the current
law projection inside its owning `BEGIN IMMEDIATE` transaction. A cutover and a
stale result therefore have one committed order across processes: a result that
commits first preceded the cutover; a result that observes the cutover refuses.
Recovery installs the successor and marks the prior active contract superseded
inside that same transaction, so no committed projection exposes two active
contracts.

### D6. Typed refusal and continuity

A stale consumer receives `stale_law_revision`, naming the old law ID and hash,
the accepted successor ID and hash, and the closed recovery actions. Continuity
exposes the same structured state. Agents do not infer the stop from prose or a
changed file.

## Rejected alternatives

- **SQLite-authored cutover:** creates a second Product-law writer and violates
  CD-0015.
- **Installation-local owner ID in Git law:** makes shared Product law depend on
  one installation's workflow memory.
- **Content-hash mismatch always blocks:** turns compatible amendments into
  breaking changes and strands valid work.
- **Advisory notice or resource claim:** cannot prevent stale workflow authority
  from accepting a result.
- **Host process termination:** belongs to host session management and cannot
  prove that stale output was excluded from Product truth.

## Verification

- The implementing change binds the current-state anchor: contract pins and their
  consumers read from current rows, with no event log or rebuild (CD-0242 D2).
- Same-ID amendments remain compatible; new-ID supersession blocks old
  consumers.
- New actions and old in-flight result acceptance fail after cutover.
- Contract recovery onto the successor and terminal work transitions remain
  available.
- A cross-process race proves one committed order between cutover and stale
  result acceptance.
