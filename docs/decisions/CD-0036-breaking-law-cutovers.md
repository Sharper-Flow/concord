# CD-0036: Breaking law supersession strictly quiesces old consumers

- **Status:** Accepted
- **Date:** 2026-08-17
- **Scope:** Product-law evolution; workflow contracts; stale-work refusal
- **Approval:** Operator approval for CD-0036
- **Related:** CD-0006 D5/D10/R3, CD-0012, CD-0013, CD-0015,
  CD-0016, CD-0027, CD-0033
- **Supersedes:** nothing; amends CD-0015's law-boundary contract

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

### D1. Law revision identity

The immutable revision identity is `(law_id, content_hash)`. `content_hash` is
the manifest-authored SHA-256 proof of the law blob. The scanned Git commit stays
audit context, not identity, because an unrelated commit changes it.

Every new law-aware workflow contract records exactly one revision pin for each
ID in its `spec_mandate`. The event log is authority. A normalized, rebuildable
contract-revision projection supports bounded reverse lookup of active
consumers.

Legacy contracts remain readable. They carry law IDs without revision pins and
use the compatibility rules below.

### D2. Amendment and replacement have different identities

A compatible amendment keeps the same stable law ID and publishes a new content
hash. Existing consumers remain valid under their recorded revision; continuity
may show that a newer compatible revision exists.

A change that requires existing consumers to reconsider their contract is not a
compatible amendment. It must publish a new law ID, mark the old record
`superseded`, and declare the existing `supersedes` relation from the accepted
successor to the old law. Every law supersession is therefore a breaking
cutover. Rename, removal, or rewritten obligation never hides behind a same-ID
overwrite when old consumers must stop.

The operator makes this legislative choice through the Git delta. Agents may
recommend a path but do not infer compatibility as workflow authority.

### D3. Strict quiescence

Once the Git-derived law projection commits a supersession, every active
workflow contract that consumes the old law is stale. No grandfather path
exists.

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

Live contract approval validates current Git-derived laws and captures their
revision pins in the same SQLite transaction that appends the approval event.
Event folding validates the recorded pin shape; it never consults today's law
projection during replay.

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

- Contract pins and reverse-consumer projection rebuild from the event log.
- Same-ID amendments remain compatible; new-ID supersession blocks old
  consumers.
- New actions and old in-flight result acceptance fail after cutover.
- Contract recovery onto the successor and terminal work transitions remain
  available.
- A cross-process race proves one committed order between cutover and stale
  result acceptance.
