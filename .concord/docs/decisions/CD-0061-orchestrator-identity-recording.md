# CD-0061: Session identity verification writes no assertion or restart boundary

- **Status:** Accepted
- **Date:** 2026-08-23
- **Scope:** Context continuity; session bootstrap; agent identity assertion
- **Approval:** Operator approval for CD-0061
- **Related:** CD-0016 (context continuity), CD-0027 (typed restart excluded),
  CD-0049 (agent delivery and identity assertion), issue #254

## Context

CD-0049 requires Concord to verify the identity a managed session presents.
Identity verification does not require a stored assertion. No projection or
authorization consumer reads the session identity assertion; recording it adds
a write and a possible failure to session preparation without informing a
decision.

CD-0027 excludes typed-lane restart and keeps `typed_availability.restart`
unavailable. The operator's session is not a native Task subagent; CD-0242 D5
keeps workflow recording with the coordinator. Neither identity verification
nor context continuity requires restart storage.

## Decision

### D1. CD-0027 excludes lane restart, and is not weakened here

CD-0027's exclusion covers restart-into-a-typed-lane: mid-execution resumption of
a worker lane carrying in-flight working memory. That exclusion stands unchanged.
`typed_availability.restart` stays a closed `unavailable` const, pinned
continuity stays re-derived per call, and nothing in this record makes lane
restart reachable.

This record states the scope CD-0027 always had. It does not narrow it, and a
future decision wanting lane restart still supersedes CD-0027 on CD-0027's own
terms.

### D2. Orchestrator identity is not recorded on a context boundary

Session preparation verifies identity without recording it, as D4 requires.
It does not populate `typed_agent_*` columns or make a clean-restart boundary
reachable.

A context boundary records a work item crossing a continuity boundary, not
session identity. A host-started session with no selected work stays
identity-only and performs no preparation (CD-0031).

### D3. The orphaned reservation is removed, not populated

The three `typed_agent_*` columns and the `restart` member of the `boundary_kind`
CHECK are removed in a forward migration. They encode a capability accepted law
excludes; retaining them would suggest that typed restart is available.

Removal hardens CD-0027's exclusion structurally, which is what that record asked
for. It does not foreclose reversal: a future decision restoring lane restart
adds its storage in the same accepted change that reopens the capability, which
is the sequence CD-0027's own consequences describe.

### D4. Identity verification records no assertion

Session preparation verifies that the active agent definition resolves, its
invocation handle matches the selected agent, and the calling host registers
that handle. Failure remains typed and refuses preparation under CD-0049.

Preparation creates no session identity assertion, source manifest, or ruleset
digest. It performs no identity write. Existing assertion receipts remain
untouched and confer no authority. Concurrent preparations must not contend on
an identity write or fail because an identity assertion ID already exists.

This retirement uses [CD-0243 D3](CD-0243-four-roles-and-six-operator-approval-classes.md#d3-decisions-outside-those-classes-belong-to-agents).
It removes the assertion and digest obligations and their tests, not identity
verification, authentication, authorization, or directory and scope checks.

## Invariants

1. No Concord code path makes lane restart reachable.
2. No durable Concord record defines an orchestrator persona.
3. A managed session that proceeds has verified the orchestrator identity;
   one that cannot verify refuses with a typed
   failure naming the required identity, the observed identity, and the paths
   searched.

## Consequences

Session preparation verifies coordinator identity without a lane-identity check
or an identity write. No entry surface performs a durable store write
(CD-0108 as amended).

The migration in D3 is forward-only and drops columns that hold no rows, so it
carries no data loss.

CD-0016's clean-restart boundary remains unreachable, which has been the accepted
position since CD-0027 and is now recorded in the schema rather than only in
prose.

## Supersession

CD-0061 supersedes nothing. It does not supersede CD-0027, whose exclusion of
lane restart D1 restates and keeps closed, and it does not supersede CD-0049,
whose D3 and Invariant 3 it implements. CD-0049's recording clauses are amended
with D4's retirement. Law changes follow CD-0243's approval boundary and the
accepted law-amendment path.

## Verification

- Concurrent preparations with a resolvable, registered active-agent definition
  succeed without changing identity receipts or the store revision.
- A prepared managed session with no resolvable orchestrator definition refuses
  with a typed diagnostic naming required identity, observed identity, and
  searched paths, and writes nothing.
- `typed_agent_type`, `typed_agent_version`, and `typed_agent_ruleset_digest` do
  not exist after the migration, and `boundary_kind` admits only `summary`.
- `typed_availability.restart` remains a closed `unavailable` const.
