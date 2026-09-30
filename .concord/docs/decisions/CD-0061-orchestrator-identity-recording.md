# CD-0061: Orchestrator identity is recorded as a session assertion, not a restart boundary

- **Status:** Accepted
- **Date:** 2026-08-23
- **Scope:** Context continuity; session bootstrap; agent identity assertion
- **Approval:** Operator approval for CD-0061
- **Related:** CD-0016 (context continuity), CD-0027 (typed restart excluded),
  CD-0049 (agent delivery and identity assertion), CD-0034 (host prompt
  provenance), issue #254
- **Supersedes:** nothing; scopes CD-0027's exclusion and amends CD-0049's
  consequences

## Context

CD-0049 D3 holds that Concord records the orchestrator identity a session
requires and verifies it. Its Invariant 3 requires a launcher-started session to
either record the typed agent identity it asserted or refuse with a typed
failure. Its Consequences state that "CD-0016's clean-restart boundary becomes
reachable" and that the D2 assertion "is what populates the reserved
`typed_agent_*` columns".

CD-0027 states the opposite outcome for those columns. It excludes typed restart,
keeps `typed_availability.restart` a closed `unavailable` const, and calls the
exclusion structural rather than a runtime choice.

Both records are accepted. Neither cites the other. CD-0049 declares no
supersession and CD-0036 lists CD-0027 only as related. An implementer reading
CD-0049 alone would write a restart boundary; an implementer reading CD-0027
alone would refuse to. Issue #254 cannot proceed while both readings are
defensible.

### The conflict is narrower than it appears

CD-0027 reasons entirely about worker lanes. It describes the excluded capability
as "resuming mid-execution into the same lane with the same scratch context",
justifies exclusion because "Concord's lanes are stateless workers whose durable
state is the workflow itself", and observes that restart "would also require a
lane-selection authority rule (which lane may resume which step)". Every premise
is lane-shaped.

The CD-0049 D3 orchestrator is not a lane. It is the operator's own session, and
it holds the authorities CD-0017 Invariant 4 forbids workers from holding. CD-0027
never considered it.

So the two records do not disagree about Product capability. They collide over
storage: CD-0016 reserved `workflow_context_boundaries.typed_agent_type`,
`typed_agent_version`, and `typed_agent_ruleset_digest` for the identity of a
*restarted lane*, gated by `CHECK(boundary_kind='restart' OR ...)`. CD-0027 then
excluded the only writer those columns were reserved for. CD-0049 needed a place
to record orchestrator identity and reached for the nearest reserved storage
without noticing it had been orphaned.

### The reserved storage is dead

`typed_agent_type`, `typed_agent_version`, and `typed_agent_ruleset_digest`
appear in exactly one place in the repository: their declaration and CHECK
constraint in `internal/store/schema.go`. No code reads them, no code writes
them, and no test references them. `foldWorkflowContextBoundaryCrossed` rejects
any boundary whose kind is not `summary`, and `ReadWorkflowContinuity` reports
restart unavailable from a hardcoded reason citing CD-0027.

Reserved storage with no writer, no reader, and an accepted decision forbidding
the only writer it was reserved for is not a foundation to build on.

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

CD-0049's consequence that the D2 assertion populates the reserved
`typed_agent_*` columns is amended: the assertion does not populate them, and
CD-0016's clean-restart boundary does not become reachable through it. The
amendment reaches that consequence only. CD-0049 D3 and its Invariant 3 stand
unchanged, and the requirement to record an asserted identity is satisfied by D4
below rather than withdrawn.

A context boundary records a work item crossing a continuity boundary. A session
identity assertion happens at launcher start, before any work item is
necessarily selected — `concord session` accepts an empty work id. Recording one
as the other would misuse the boundary sequence and require a `restart` boundary
kind that D1 keeps closed.

### D3. The orphaned reservation is removed, not populated

The three `typed_agent_*` columns and the `restart` member of the `boundary_kind`
CHECK are removed in a forward migration. They encode a capability accepted law
excludes, and retaining them invites exactly the misreading CD-0049 made.

Removal hardens CD-0027's exclusion structurally, which is what that record asked
for. It does not foreclose reversal: a future decision restoring lane restart
adds its storage in the same accepted change that reopens the capability, which
is the sequence CD-0027's own consequences describe.

### D4. A session assertion is recorded as a domain event

The asserted orchestrator identity is recorded as a domain event with
`subject_type='session'`, carrying the asserted type, version, and ruleset
digest. Events are Concord's durable monotonic spine, they need no new table, and
they do not constitute a definition of an orchestrator persona, so CD-0049
Invariant 4 and CD-0016's exclusion of an `agent_definitions` table both hold.

The event records what was asserted at a moment. It is evidence, not
configuration, and nothing reads it back to decide what a session may do.

### D5. The ruleset digest covers the host artifacts the assertion observed

The orchestrator ruleset digest is a SHA-256 over the manifest of host-supplied
artifacts the assertion resolved: the orchestrator definition file and the
instruction chain it loads, each contributing kind, path, and content hash.

This is the construction `computeHostPromptProvenance` already uses for CD-0034
host prompt provenance, and it is the only honest answer available. A lane digest
covers a Concord-generated contract body because Concord authors that body. D1 of
CD-0049 places orchestrator authorship host-side, so there is no Concord artifact
to digest, and a digest over bytes Concord did not author is a record of what was
observed rather than a pin on what was approved.

## Invariants

1. No Concord code path makes lane restart reachable.
2. No durable Concord record defines an orchestrator persona.
3. A launcher-started session that proceeds has recorded the orchestrator
   identity it asserted; one that cannot assert refuses with a typed failure
   naming the required identity, the observed identity, and the paths searched.
4. The orchestrator ruleset digest is derived only from artifacts the assertion
   actually resolved, never from a declared or expected value.

## Consequences

Issue #254 becomes implementable: the assertion slots beside the existing lane
identity hook in `runSessionCommand`, and its record has a home that no accepted
decision forbids.

`concord session` acquires a durable write. It performs only reads today, so the
session path gains a store write boundary it did not previously have.

The migration in D3 is forward-only and drops columns that hold no rows, so it
carries no data loss.

CD-0016's clean-restart boundary remains unreachable, which has been the accepted
position since CD-0027 and is now recorded in the schema rather than only in
prose.

## Supersession

CD-0061 supersedes nothing. It does not supersede CD-0027, whose exclusion of
lane restart D1 restates and keeps closed, and it does not supersede CD-0049,
whose D3 and Invariant 3 it implements. It amends one consequence of CD-0049, as
described in D2, by the same amendment path CD-0017 and CD-0018 use for CD-0005.
Changes to D1-D5 follow the accepted decision-supersession path and record
explicit operator acceptance.

## Verification

- A launcher-started session with a resolvable orchestrator definition records a
  `subject_type='session'` event carrying type, version, and ruleset digest.
- A launcher-started session with no resolvable orchestrator definition exits
  non-zero with a diagnostic naming required identity, observed identity, and
  searched paths, and writes no event.
- `typed_agent_type`, `typed_agent_version`, and `typed_agent_ruleset_digest` do
  not exist after the migration, and `boundary_kind` admits only `summary`.
- `typed_availability.restart` remains a closed `unavailable` const.
- The recorded ruleset digest equals a digest recomputed from the resolved
  artifacts, and changes when any resolved artifact changes.
