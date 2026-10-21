# CD-0218: The live workflow catalog retains three execution families

- **Status:** Accepted
- **Date:** 2026-10-09
- **Scope:** Live workflow catalog, typed authoring, and authorized cutover. No
  live migration or release activation is authorized.
- **Classification:** Breaking replacement under CD-0036 D2
- **Planning record:** [Concord (CON) issue 902](https://linear.app/sharper-flow/issue/CON-902/delete-redundant-workflow-ceremony-retain-minimal-authorized-agent)
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2, D13, D14
- **Approval:** The operator approved this successor as designed on 2026-10-09
  at 19:35 Eastern Daylight Time (EDT), relayed through the Snowball
  coordinator. This publication is the isolated candidate only: it does not
  update a serving Product's law projection, disposition live consumers, or
  activate a release.
- **Candidate supersession set:** CD-0007, CD-0012, CD-0013, CD-0039,
  CD-0077, CD-0115, CD-0156, CD-0198. This set comes from the approved design;
  the D5 owner must resolve its current successor identities before cutover.
- **Amends:** CD-0115, which
  [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)
  supersedes in full;
  [D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)
  keeps current state and finite snapshots instead of retained historical
  readers, and the cutover follows
  [D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot):
  one documented migration with a verified snapshot and the operator's
  approval of its destructive operation.
- **Related:** CD-0007, CD-0012, CD-0013, CD-0036, CD-0039, CD-0077,
  CD-0115, CD-0156, CD-0172, CD-0186, CD-0198, CD-0215

## Context

The live workflow catalog requires seven families. Four families repeat bounded
job, independent evidence, and delivery duties that the retained execution owners
already carry. Their public authoring can retire; [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) and [D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot) govern: the store keeps current state and finite snapshots, and the one documented cutover retires what the migration discards.

This document proposes a replacement, not a same-ID amendment of removed duties.
The replacement does not make research conclusions into binding Product law.
The accepted research-pack capability remains independent of workflow selection.

## Decision

Only the three live execution families receive new `approval_policy=consequence_v1` versions under CD-0013 D11. [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: the store keeps current state and finite snapshots, not frozen history, and recorded work keeps its recorded consent and attribution facts. New-policy verdicts require independent authenticated evaluators without operator substitution; completion and engineering confirmation use their real authorized coordinators. No old work migrates implicitly and no retired family gains a new authoring route.


### D1. Three families own new execution

New execution selects `workflow.implementation`, `workflow.break_fix`, or
`workflow.research`. The live catalog does not select architecture_spike,
ops_runbook, static_analysis, or generic_one_off. No compatibility alias maps a
retired family to a retained one.

Task capture defaults to implementation. Bug capture defaults to break-fix.
Research capture defaults to research. Decision and other captures require an
explicit retained family. Operator kind and workflow family remain different
vocabularies. A retired ref refuses before any capture or contract write.

Implementation and break-fix allow exists, absent, and check predicates. Research
allows outcome predicates with no_change, resolved, and report_recorded tokens.
New authoring does not admit a decision_record or accepted_decision,
insufficient_evidence, remediated, completed, or operator_defined outcome token.
The independent insufficient_evidence verdict remains a truthful refusal.

Research reports about architecture or analysis do not enact legislation.
Product-law changes require the existing operator-approved Git law route.
Removing ops choreography adds no provider executor, health observer, rollback
claim, timer service, or new authority for external effects.

### D2. Fewer calls carry the same enforceable facts

Break-fix records one typed diagnosis with required reproduction, backlog-search,
and root-cause sections. Missing or inconsistent sections refuse atomically.
The search retains searched, outcome, and related-ID consistency. Its fold writes
the existing alignment projection and creates no automatic relation.

Implementation retains proposal, mandatory alignment, typed design, contract
approval, execution, refinement, delivery, independent acceptance, and closure.
Its empty discovery action and graph node retire. Source-qualified investigation
findings remain in typed work context. Alignment precedes analysis and design.

Repair and refinement starts derive once from the first admitted dispatch or
matching effect-class checkpoint at that step. The first event's resolved actor
owns the fence. The derived fact preserves the original log order and epoch.
Context, job, evidence, rejection, and final-delivery records cannot start or
reset the epoch. Refused actions leave no derived start. Correction re-entry
uses the existing declared epoch owner. Pre-start verification remains stale.

A terminal failed attempt supplies its workflow failure/correction fact once,
from the original cause and evidence. A completed result's rejection is
not a terminal worker failure. Neither fact grants new dispatch authority.
This successor explicitly extends derivation to retained research. CD-0198's
implementation/break-fix approval alone does not authorize that extension.
Research requires a new definition and its own failure/retry/rebuild witnesses.

The actor of a derived `record_worker_failure` is the `actor_ref` of the latest
matching `dispatch_worker` completion for that attempt. The terminal producer's
identity does not select the disposer. This actor holds the fenced
checkpoint-writing authority after disposition until the next dispatch re-pins
a lane. Other actors, including the disposed lane, cannot write that fenced
checkpoint.

Result rejection may carry a bounded retry lane. One transaction records the
rejection and an admitted fresh dispatch intent through the existing dispatch
owner. Admission refusal records neither. Host launch remains outside SQLite.
Launch failure retains the committed facts and truthful failure. Lost
acknowledgement recovers the original attempt rather than spawning another.
At the retry wall the combined request refuses; rejection-only remains reachable.
Required operator approval and evidence-backed convergence remain unchanged.

An accepted report may assert producer-step delivery through the existing full
delivery admission. Local acceptance never substitutes for integration, settling
review, independent verification, final delivery evidence, or whole-work closure.
The final delivery gate keeps its coordinator assertion and existing exit.

New verdict authoring carries only a verdicts array. Every entry has its own
predicate, actor, evidence, and verdict. Duplicate IDs refuse. Omitted predicates
remain missing. New contract authoring carries only explicit outcome_predicates.
No default token or synthetic check supplies an omitted approved end-state.

### D3. Authorization and correction keep their owners

Signed contract approval and the signed verdict checkpoint remain separate from
independent verdict evidence. Immutable job revisions, attempts, subject commits,
packet/report identities, actor/session/Project/worktree bindings, law/research
pins, and operation/idempotency keys remain executable admission facts.

One admission owner serves dispatch, delivery, correction, and closure; [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: admission validates current rows in one transaction, with no folded event validation. Host preauthorization remains at its own
boundaries. No caller-supplied echo replaces dispatch-owned provenance.

Complete-step contract correction remains confined to implementation and
break-fix under CD-0172 and CD-0186. The declared producer return supplies fresh
design and proof after correction. Evidence binding can discharge an unverdicted
active predicate through its existing owner. No acceptance-step bypass, stale
design heuristic, or weakened evidence-kind check replaces that route.

Ordinary stale-law and stale-Domain re-pinning keeps its existing admission at
eligible running steps of every retained workflow family, including research.
Duplicate-contract recovery and currently eligible earlier-step correction also
keep their existing admission. CD-0186 limits the complete-step disproved-premise
correction route, not ordinary stale recovery. Preserve both independently; an
ordinary re-pin does not manufacture a producer return or evade the correction
gate. Cancellation does not replace an existing admitted re-pin exit.

Exact shared-write conflicts, exclusive claims, session/worktree placement,
cross-repository handoff, uncertain-effect recovery, typed shared context,
research-pack provenance and consumers, and GitHub delivery receipts remain.
Shared affected-Domain membership alone grants no effect authority and creates
no compatible-with approval requirement.

### D4. Frozen history remains exact and readable

[CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)
and
[D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)
govern: the store keeps current state and verified finite snapshots, not
frozen history, and derived definitions are generated from one canonical
source with no released version retained for history.

### D5. Cutover is an operator-owned boundary

[CD-0242 D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot)
governs: the cutover is one documented migration that inventories what
survives, preserves every worktree and its uncommitted files, verifies the
target, and needs the operator's approval of its destructive operation.

### D6. Unchanged obligations must survive publication

The candidate supersession set names records, not clause-level runtime overrides.
Before publication, carry every unaffected obligation from those records into
the accepted successor text or an approved coherent successor set. Retain the
bootstrap/platform floor, explicit end-state and independent-verdict rules,
research-pack guarantees, vocabulary ownership, the identities active work
needs, mandatory backlog search, and all CD-0198 evidence/approval/retry floors.

Do not publish this draft with only a prose exception to still-accepted rules.
The publication owner must update old record status, manifest identities,
content hashes, successor relations, and consumer-cutover evidence together.
Other simplification scopes must use the same legislative identities rather
than publish competing overrides.

## Acceptance Criteria

```gherkin
  Given a new capture names a retired workflow family
  When the authenticated capture route validates it
  Then the route refuses before any work or contract write
```

```gherkin
  Given a break-fix diagnosis lacks its search section
  When the coordinator records the diagnosis
  Then no diagnosis, alignment, or step advance commits
```

```gherkin
  Given rejection and admitted retry intent committed before acknowledgement loss
  When the coordinator reconciles the original operation
  Then the retained attempt is returned without another host launch
```

```gherkin
  Given a completed item recorded under a retired-family definition
  When the store reads that item back
  Then its recorded digest, actor, subject, and evidence remain unchanged
```

```gherkin
  Given a nonterminal retired-family pin has no approved cutover disposition
  When the simplified release checks activation readiness
  Then activation refuses before a live store mutation
```

```gherkin
  Given a retained change lacks an independent current predicate verdict
  When the coordinator requests whole-work completion
  Then completion refuses and names the missing predicate
```

```gherkin
  Given a running retained-family item has an ordinary stale-law recovery route
  When the operator approves its exact successor contract at an eligible step
  Then the ordinary re-pin commits without requiring disproved-premise correction
```

```gherkin
  Given a terminal failed attempt whose dispatch actor differs from its terminal producer
  When the store derives its workflow failure record
  Then the record actor is the matching dispatch completion's actor
  And only that disposer may write the fenced checkpoint until the next dispatch
```

## Alternatives considered

- Delete historical definitions and recover them manually from Git. [CD-0242 D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot) governs: the one documented cutover migration, not manual recovery, retires what it discards.
- Remap old families to implementation. The replacement would change authority,
  actors, graph exits, and the meaning of existing evidence.
- Replace typed records with a progress field bag. Structural admission would
  become a model claim.
- Remove independent acceptance or its signed checkpoint. Fewer tool calls do
  not discharge those authorization duties.
- Publish breaking clauses as same-ID amendments. CD-0036 forbids this route.

## Consequences

This record publishes the approved successor text as isolated candidate law. It
does not update a serving Product's law projection, disposition any active
consumer, or activate a release. Live supersession of the candidate set happens
only at the D5 boundary.

Old record status, manifest identities, content hashes, successor relations, and
consumer-cutover evidence update together at that boundary, never before. Until
then the set names records, and no superseded status or supersedes relation
exists in the manifest for this record.

Every unaffected obligation in the set keeps its owning record until that
boundary. CD-0007 keeps the bootstrap, platform, and authority duties.
CD-0012 keeps explicit end-state, non-vacuity, immutable approval, and
independent acceptance. CD-0013 keeps current-state workflow recording,
declared actions, and independent acceptance. CD-0039 keeps D1 through D4 and D7 through
D9 for historic attributed reports, failures, and effects.

CD-0077 keeps operator kind separation, stored historical names, and immutable
migration checksums. CD-0156 keeps mandatory backlog search. CD-0198 keeps every
evidence, approval, start, and retry floor. CD-0215 owns the research-pack
guarantees, and this record leaves it standing.

CD-0215 D2 still requires a separate architecture spike decision. Before cutover,
the approved successor set resolves that obligation.
This candidate does not change CD-0215.

[CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) supersedes CD-0115 in full and resolves the CD-0115 amendment:
[D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) keeps current state and finite snapshots, and
[D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source) generates derived definitions from one source.
Retired families keep no live capture eligibility; that refusal activates with the
set at the D5 boundary.

## Verification

- `python3 scripts/check-doc-contract.py` proves this record carries the current
  decision outline, its criteria grammar, and the writing rules.
- `python3 scripts/check-knowledge-index.py` proves the record shard, the
  content hashes, and the composed aggregate agree.
- `python3 scripts/check-knowledge-closure.py` proves no unprocessed document
  remains under the knowledge roots.
- `python3 scripts/check-law-coverage.py` validates this record's executable
  evidence anchors.
- `python3 scripts/check-doc-links.py` and `python3 scripts/check-knowledge-placement.py`
  prove document links and knowledge placement hold.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0218
  identifier allocates once across the fetched peer refs.
- Criterion 1 is covered by
  `internal/agent.TestCaptureLiveAuthoringSelectorRefusesRetiredFamiliesAndUnnamedDecisionKinds`.
  Authenticated capture of each retired family refuses with unchanged work,
  contract, workflow-instance and event counts, and unchanged database write counts.
- Criterion 2 keeps its diagnosis anchors:
  `go test ./internal/store/ -run TestRecordDiagnosisRefusalsLeaveZeroWrites`
  and `go test ./internal/store/ -run TestRecordDiagnosisRecordsAllSectionsAtomically`.
- Criterion 3 keeps its retry anchors:
  `go test ./internal/store/ -run TestCombinedRejectRetryDispatchesThroughTheDispatchOwner`
  and `go test ./internal/store/ -run TestRetryLaunchFailureAfterCommittedIntentStaysTruthful`.
- Criterion 4's recorded-definition reading binds the current-state anchor:
  recorded definitions and their digests read back unchanged, with no replay
  (CD-0242 D2, D14). Until that change lands,
  `python3 scripts/check-knowledge-index.py` is the executable anchor for this
  record's law registration.
- Criterion 5 is covered by
  `internal/store.TestCutoverRefusesNonterminalArchivedPinAtReadiness` and
  `internal/store.TestCutoverAllowsTerminalHistoricalPinsAtReadiness`.
  Readiness refuses the nonterminal archived pin without a live store mutation;
  terminal recorded items remain readable as current rows (CD-0242 D2).
- Criterion 6 keeps its completion anchor:
  `go test ./internal/store/ -run TestWorkflowCompletionRequiresCurrentExplicitImpactVerdict`.
- Criterion 7 keeps its ordinary-recovery anchors:
  `go test ./internal/store/ -run TestStaleRegistryRescanAdmitsExactlyOneRePinRoute`
  and `go test ./internal/store/ -run TestCompleteStepCorrectionStaleLawStaysBehindTheGate`.
- Criterion 8's derived-failure actor and fenced checkpoint anchors are:
  `go test ./internal/store/ -run TestDerivedWorkerFailureRecordsOnceFromTheOriginalTerminalEvent`,
  `go test ./internal/store/ -run TestDerivedDispositionWriterAdmitsItsOwnFencedCheckpoint`,
  `go test ./internal/store/ -run TestDerivedDispositionRefusesTheDisposedLaneCheckpoint`
  and `go test ./internal/store/ -run TestDerivedDispositionWriterLosesThePenAtTheNextDispatch`.
  The last two refuse the disposed lane's checkpoint and, after the next dispatch
  re-pins a lane, the former disposer's checkpoint.

Retained research failure derivation is covered by
`internal/store.TestResearchDerivedFailureRetriesAndRebuilds`.
These runtime witnesses establish enforcement, not live consumer disposition,
supersession publication or release activation. D5 still governs those operator actions.
