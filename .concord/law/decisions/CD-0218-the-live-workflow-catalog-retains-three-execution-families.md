---
{
  "id": "CD-0218",
  "title": "The live workflow catalog retains three execution families",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": []
}
---
# CD-0218: The live workflow catalog retains three execution families

- **Status:** Accepted
- **Date:** 2026-10-09
- **Scope:** Live workflow catalog, typed authoring, and authorized cutover. No
  live migration or release activation is authorized.
- **Planning record:** [Concord (CON) issue 902](https://linear.app/sharper-flow/issue/CON-902/delete-redundant-workflow-ceremony-retain-minimal-authorized-agent)
- **Approval:** The operator approved this decision on 2026-10-09
  at 19:35 Eastern Daylight Time (EDT), relayed through the Snowball
  coordinator. Live migration and release activation require separate authority.
- **Candidate supersession set:** CD-0007, CD-0012, CD-0013, CD-0039,
  CD-0077, CD-0156, CD-0198. The D5 owner must resolve its current successor
  identities before cutover.
- **Related:** CD-0007, CD-0012, CD-0013, CD-0036, CD-0039, CD-0077,
  CD-0156, CD-0172, CD-0198, CD-0215

[CD-0243 D6](CD-0243-four-roles-and-six-operator-approval-classes.md#d6-existing-approval-requirements-follow-these-classes)
governs D1: Product-law changes require approval only under its six classes. Workflow-family and repository-law-route requirements remain required.

[CD-0243 D7](CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
qualifies every operator-consent clause in this record, including tables and
acceptance criteria. Other admission, safety, evidence, and historical-pin
requirements remain required.

## Context

The live workflow catalog requires seven families. Four families repeat bounded
job, independent evidence, and delivery duties that the retained execution owners
already carry. Their public authoring can retire; the store keeps current state and finite snapshots, and the one documented cutover retires what the migration discards ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) and [D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot)).

Research conclusions do not enact binding Product law.
The accepted research-pack capability remains independent of workflow selection.

## Decision

The store keeps current state, finite snapshots, and attribution facts ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). Review inspects the accumulated solution, verifies the corrected candidate, and reports one disposition that Main records; completion and premise confirmation use their real authorized coordinators ([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)). No retired family gains a new authoring route.

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

The live families are implementation, break-fix, and research, each family's plain definition sets its mandatory stages, break-fix keeps diagnosis and regression proof, and no agent waives a mandatory stage or an operator approval ([CD-0242 D4](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d4-families-compose-shared-stages-and-rules-set-the-minimum)).

### D3. Authorization and correction keep their owners

Attributed contract recording and premise confirmation remain separate from
independent verdict evidence. Immutable job revisions, attempts, subject commits,
packet/report identities, actor/session/Project/worktree bindings, research
pins, and operation/idempotency keys remain executable admission facts.

One admission owner serves dispatch, delivery, correction, and closure; admission validates current rows in one transaction ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). Host preauthorization remains at its own
boundaries. No caller-supplied echo replaces dispatch-owned provenance.

Complete-step contract correction remains confined to implementation and
break-fix under CD-0172. The declared producer return supplies fresh
design and proof after correction. Evidence binding can discharge an unverdicted
active predicate through its existing owner. No acceptance-step bypass, stale
design heuristic, or weakened evidence-kind check replaces that route.

Duplicate-contract recovery and currently eligible earlier-step correction also
keep their existing admission. Law and registry changes require no contract
re-pin; they reconcile through Git under CD-0036. This does not weaken
CD-0172's complete-step correction gate or manufacture a producer return.

Exact shared-write conflicts, exclusive step claims, host-owned session/worktree
placement under CD-0088 D1,
cross-repository handoff, uncertain-effect recovery, typed shared context,
research-pack provenance and consumers, and GitHub delivery receipts remain.
Shared affected-Domain membership alone grants no effect authority and creates
no compatible-with approval requirement.

### D4. Recorded work reads back exactly

The store keeps current state and verified finite snapshots ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

### D5. Cutover is an operator-owned boundary

The cutover is one documented migration that inventories what survives, preserves every worktree and its uncommitted files, verifies the target, and needs the operator's approval of its destructive operation ([CD-0242 D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot)).

### D6. Unchanged obligations must survive publication

The candidate supersession set names records, not clause-level runtime overrides.
Before publication, carry every unaffected obligation from those records into
the accepted successor text or an approved coherent successor set. Retain the
bootstrap/platform floor, explicit end-state and independent-verdict rules,
research-pack guarantees, vocabulary ownership, the identities active work
needs, mandatory backlog search and CD-0198's evidence and approval floors.
Native Task remains host-owned; workflow result recording keeps scope and
safety checks ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-native-task-belongs-to-the-host)).

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
  Then any retained digest, actor, subject and evidence remain unchanged
  And execution state collected under CD-0242 D2 returns typed absence with the canonical outcome locator
  And the reader never fabricates historical evidence or a live artifact reference
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

## Alternatives considered

- Delete historical definitions and recover them manually from Git. The one documented cutover migration, not manual recovery, retires what it discards ([CD-0242 D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot)).
- Remap old families to implementation. The replacement would change authority,
  actors, graph exits, and the meaning of existing evidence.
- Replace typed records with a progress field bag. Structural admission would
  become a model claim.
- Remove independent acceptance or its signed checkpoint. Fewer tool calls do
  not discharge those authorization duties.
- Publish breaking clauses as same-ID amendments. CD-0036 forbids this route.

## Consequences

Live law publication, consumer disposition, and release activation require D5's authority.
At that boundary, old record status, manifest identities, content hashes,
successor relations, and consumer-cutover evidence update together, never before.

CD-0007 governs bootstrap, platform, and authority duties.
CD-0012 governs explicit end-state, non-vacuity, immutable approval, and
independent acceptance. CD-0013 governs current-state workflow recording,
declared actions, and independent acceptance. CD-0039 D1 through D4 and D7 through
D9 govern attributed reports, failures, and effects.

CD-0077 governs operator kind separation, stored historical names, and immutable
migration checksums. CD-0156 governs mandatory backlog search. CD-0198 governs
evidence, approval, and start floors. CD-0215 governs research-pack guarantees.

CD-0215 D2 requires a separate architecture spike decision. D5 cutover must
resolve that obligation through an approved successor set.

Retired families keep no live capture eligibility; that refusal activates with the
set at the D5 boundary.

## Verification

- Committed law-folder snapshot validation proves the record's metadata,
  content digest, and folder placement agree.
- `python3 scripts/check-doc-links.py` proves document links hold; law
  placement is folder membership under `.concord/law/`.
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
- Criterion4 distinguishes retained-state readback from collected-state absence under CD-0242 D2 and D14.
  Retained definitions, digests, actors, subjects and evidence are read as recorded, without replay or relabeling.
  Collected execution state returns typed absence and its canonical outcome locator.
  `internal/store.TestPublishedExecutionAbsenceReaders` and `internal/store.TestDefectRetirementCollectedAbsenceReaders` bind the collected-state witnesses; retained-definition witnesses remain required.
- Criterion 5 is covered by
  `internal/store.TestCutoverRefusesNonterminalArchivedPinAtReadiness` and
  `internal/store.TestCutoverAllowsTerminalHistoricalPinsAtReadiness`.
  Readiness refuses the nonterminal archived pin without a live store mutation; terminal items return unchanged retained state or typed collected-state absence under CD-0242 D2.
- Criterion 6 keeps its completion anchor:
  `go test ./internal/store/ -run TestWorkflowCompletionRequiresCurrentExplicitImpactVerdict`.
- Criterion 7 keeps its ordinary-recovery anchors:
  `go test ./internal/store/ -run TestStaleLawHoldsLateVerdictUntilRePin`
  and `go test ./internal/store/ -run TestCompleteStepCorrectionStaleLawStaysBehindTheGate`.

Retained research failure derivation is covered by
`internal/store.TestResearchDerivedFailureRetriesAndRebuilds`.
These runtime witnesses establish enforcement, not live consumer disposition,
supersession publication or release activation. D5 still governs those operator actions.
