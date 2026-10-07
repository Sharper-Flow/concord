# CD-0209: Declared recovery routes preserve artifact truth

- **Status:** Accepted
- **Date:** 2026-10-07
- **Scope:** The one declared recovery-route table per workflow definition, the
  closed two-trigger contract, the registration refusals, the one reader every
  runtime cross-step correction feeds from, the producer mechanism the route
  target must carry, the artifact-staleness fold and its admission halves, the
  work-pin sequence that names a return, the liveness and conformance checks,
  and the liveness-on-release pass for the 108 released definition versions
- **Extends:** CD-0201 D1 with the typed route table and the producer rule;
  CD-0201 D2 with the bit the liveness and conformance checks read; CD-0201
  D3, D4 unchanged
- **Amends:** CD-0143 D1 at its return target: the engine-owned
  request_correction route reads the declared target rather than a work-kind
  step order, and the unhealthy-verdict route is the one path the engine
  admits after an accepted worker delivery with a current unhealthy verdict;
  CD-0201 D1 at its folded-state coverage to include the artifact-staleness
  frontier and the production frontier; CD-0204 D3 at its host claim: every
  late-verdict step that the closed trigger unhealthy_verdict serves sits on
  a step that declares the table's return, and the late record_verdict route
  reaches the producer through the same reader
- **Preserves:** CD-0172 D1/D2 and CD-0186 in full, including the
  complete-step supersede limit to implementation and break_fix, the
  convention-cutoff exact approval terminal, and the refusal of any other
  family or step on that route; CD-0143 D2, the return to the declared
  external effect; CD-0143 D3, the bound on correction sequences; CD-0143 D4,
  the fail-closed completion; CD-0164 population and wall keyed by the
  declared target; CD-0115 D1, the frozen released definition content and
  digests; CD-0122 D2, the outside-repair authorization this amendment runs
  under; every other route refusal on every other route (CD-0186)
- **Related:** CD-0013, CD-0059, CD-0067, CD-0115, CD-0116, CD-0122, CD-0124,
  CD-0133, CD-0137, CD-0138, CD-0143, CD-0147, CD-0159, CD-0164, CD-0166,
  CD-0172, CD-0173, CD-0186, CD-0187, CD-0193, CD-0197, CD-0201, CD-0203,
  CD-0204, CD-0206, CD-0207, and Concord (CON) issue 861
- **Approval:** The operator authorized completion of the
  declared-recovery-route contract under CD-0122 D2 as outside repair for
  CON-861. The latest managed proposal for CON-861, not a fabricated
  workflow record, is the source this amendment serves.

## Context

CD-0201 D1 puts workflow action admission in one function over one folded
state. CD-0201 D2 binds the liveness law over every registered definition
version. CD-0201 D3 settles the post-rejection review debt on a ship or
absent verdict. CD-0204 folds the acceptance-deliverables refusal and the
late record_verdict route into the same admission owner.

A fresh review of the recovery half of CD-0201 found that the contract was
incomplete. Cross-step correction was admitted in five shapes:

1. The post-`request_correction` return to a `human_checkpoint` step, which
   CD-0143 D2 fixes by the work-kind's external-effect step.
2. The evaluator's unhealthy-verdict return from `verify` or `release` or
   `complete`, which read from a per-family step order, with `workflow.ops_runbook`
   and `workflow.static_analysis` taking different producer steps than
   `workflow.generic_one_off` even when their graphs agreed.
3. The parked CD-0166 delivery gate, which walked one forward edge and
   resolved the verdict step's route from a nearest-effect step slice.
4. The pinned complete-step contract correction under CD-0172, which
   required the implementation or break_fix family and refused the others
   (CD-0186).
5. The CON-846 trap, where a released generic_one_off pin under an
   approved successor contract resolved no return at all because the
   quarantine had not been authored.

Each shape carried its own step-order rule, and the result was three
independent failures on a single defect:

- The CON-846 shape: a released evaluator step with an unhealthy verdict
  and no admitted route to its producer.
- The CON-861 shape: a stale artifact that the verifier's independent
  judgment could not refuse, because the folded state did not carry the
  causal origin of the production event.
- The bad-to-ok transition CD-0201 forbids: a `record_verdict` action that
  moved the work from a stale state to an unstale state without
  re-production at the declared producer.

The structural fix is one table. The runtime reads the table through one
function; registration validates the table through one gate; the artifact
fold reads the table to bind the production frontier to the declared
target; the work pin reads the table to name the next sequence. The
acceptor must know the target before it can admit the return, and the
admission model must know the producer before it can decide whether a
move would re-produce the artifact. A graph walk, a work-kind heuristic, a
whitelist, or a nearest-target order does not own that decision.

## Decision

### D1. One declared recovery-route table per family

A workflow definition declares at most one recovery-route table
(`WorkflowRecoveryRoute[]`) under the schema-version 1.3 key
`recovery_routes`. The field is optional: an authored version may declare
it, a released version does not, and the canonical-bytes function emits it
under the `omitempty` tag so the absence of the field on a released
manifest does not change its digest (CD-0115 D1). Each route is a typed
record with four closed fields:

| Field | Type | Meaning |
|---|---|---|
| `Step` | step id | The step the route returns from: the step that declares `record_verdict` or `confirm_premise` for an unhealthy verdict, the late-verdict step (CD-0204) for a late unhealthy verdict, or the step that declares `complete` for a premise disproved at the pinned complete step. |
| `Trigger` | enum | The closed condition that opens the return. Only `unhealthy_verdict` and `disproved_premise_at_complete` are valid. |
| `Action` | enum | The correction action the route admits at the step. The trigger pairs the action: `unhealthy_verdict` admits `request_correction`; `disproved_premise_at_complete` admits `supersede_contract`. The pairing is total. |
| `Target` | step id | The producer step the route returns to: the step that re-produces the artifact the evaluator judges. |

A route that names a step that does not declare `record_verdict` and does
not declare `confirm_premise` and is not a terminal or late-verdict step
(CD-0204) refuses at registration for `unhealthy_verdict`. A route that
names a step that does not declare `complete` refuses at registration for
`disproved_premise_at_complete`. The action must match the trigger's
pair; the target must reach the route step through forward edges alone
(producer rule); the target must not be the route step; the target must
not be terminal; the target must not declare `approve_contract`; the
target must carry an artifact-producing mechanism (D4); the route must not
duplicate for one (step, trigger) pair. Same-step attempt recovery —
`failed_attempt`, `rejected_attempt`, review-debt, the late-record
evidence path — stays with its existing owner and declares no route, so
the runtime cross-step reader does not see it and the gate does not
delegate a same-step return through the table.

Engine-owned recovery actions resolve off a pinned step through
`workflowRecoveryActionDefinition`. A definition does not need to list
`request_correction` or `supersede_contract` at root for a route to name
them; the route declaration itself admits the engine action off the
pinned step. The closed route action set is the engine's recovery-action
set, and a route that names an action outside the set refuses at
registration.

The closed two-trigger set is the only pair the runtime cross-step reader
recognizes. Same-step attempt recovery keeps its own owner (the
attempt-failure chain and the review-debt chain). The gate delegates to
the evaluator's declared unhealthy-verdict target; the gate does not walk
the graph, does not order the step slice by a kind, and does not pick a
nearest-effect step.

### D2. The registration gate refuses an undeclared return

A definition that names no recovery-route table and that has no
table-less released (ref, version) in the quarantine refuses registration
the moment any of its steps records a verdict, confirms a premise, or
hosts the late-verdict context. The coverage clause is the one clause
the gate owns. A released version whose (ref, version) the quarantine
does not name resolves no routes and refuses the same way, so a stranded
pin on an unknown ref or version is not admitted with an empty table.
A released version whose (ref, version) the quarantine names — the
generic_one_off 1-to-13 range, the implementation 1-to-22 range, the
break_fix 1-to-19 range, the research 1-to-13 range, the
architecture_spike 1-to-14 range, the ops_runbook 1-to-15 range, and the
static_analysis 1-to-12 range, the 108 released versions in total —
resolves the quarantined routes, and the registration gate validates the
resolved routes against that version's own graph. A released version
whose graph yields a different route than its quarantine holds refuses
the same way, so the quarantine and the graph cannot drift.

The gate also refuses:

- an ambiguous action or trigger target — a route declared twice for one
  (step, trigger) pair, or a target the graph does not name;
- an absent producer — a target that carries no `dispatch_worker`, no
  fenced-start action, and no direct `record_finding`, `record_decision`,
  or `record_delivery` action;
- a non-upstream target — a target that does not reach the route step
  through forward edges;
- two failure edges on one step — the registration-level bound every
  released version also satisfies;
- an engine-unknown route action — an action outside the engine's closed
  recovery-action set.

The quarantine range is the released-version-only contract for each
family. The published numbers above are golden: a change to the range
moves the digests of every released version above it through a new
content freeze, which CD-0115 D1 forbids. The quarantined data
themselves — the per-(ref, version) route slice — are golden per
version. Authored versions declare their own table; the quarantine never
guesses a table from a graph, a work kind, or a step slice.

### D3. One reader, one function, no whitelist

The runtime cross-step correction reader is
`workflowRecoveryRoutes(definition)`. The function returns a defensive
copy: a nil or empty declared table falls through to the quarantine for
the (ref, version), a declared table is the one owner of its (ref,
version), and an unknown ref or a version above the quarantine range
resolves nil. No caller walks the graph, orders the step slice, or
reads a work kind to pick a target. No whitelist of step kinds or family
names overrides the table. No nearest-effect step or nearest-verdict
walk derives a return.

The fold, the action guards, the work pin intents, the dispatch fold,
the correction binding, the CD-0164 correction counting and three-attempt
wall, the CD-0172 complete-step return, the late `record_verdict` route
of CD-0204 D3, and the liveness and conformance replay all read through
this one function. The folded state the admission model carries
(`RecoveryRoutes`) is the same slice the runtime reads, so the recorded
move and the admitted intent cannot disagree about the return.

### D4. The producer rule names the artifact-producing mechanism

A route target is a producer when at least one of the following holds:

- the step declares `dispatch_worker` — a dispatch at the target step
  binds a worker attempt whose acceptance at the target step
  (`accept_worker_result`) is the production event;
- the step declares an action whose execution mode is `fenced`,
  which can open the attempt that later produces the artifact;
- the step declares one of the closed positive production actions
  `record_finding`, `record_decision`, or `record_delivery` — each is a
  direct artifact completion the step owns;

A fenced start alone is never a production event. A start records
intent; the production frontier reads the accepted worker delivery or
the closed positive production action, not the start. A start with no
follow-up completion is a stranded step and the liveness check names
it. A `dispatch_worker` completion alone, with no `accept_worker_result`
at the target, is a dispatch; it is not production. A verdict, an
evidence binding, a refinement review acceptance, a candidate revision,
an impact declaration, a successor link, a checkpoint, or a verdict
recording is never production, whatever its mode. The closed
positive-production classification is the only production rule, and a
family that gains a new producing mechanism declares it here; nothing
else changes.

The worker production frontier reads three things and three things only:
the accepted `accept_worker_result` at the target step, the
`dispatch_worker` completion at the target step for the same worker
attempt, and the capability dispatch event that carries one of
`implementation`, `design`, or `research` as the capability class for
that attempt. The dispatch and the capability events must precede the
acceptance in the same event history, and all three must postdate the
stale cause. A capability class outside the producing set fails closed:
identity and class fail closed together, so a dispatch with an
unrecognized class never refreshes the artifact. A review or
verification lane dispatch — the lanes whose class is not in the
producing set — never refreshes the artifact either, whatever the
work-kind. The typed target production direct action owns proof of
production: a `record_finding` at the research producer, a
`record_decision` at the architecture-spike producer, a
`record_delivery` at every other producer, each with its own owning
	semantics.

### D5. The artifact-staleness fold is the folded state the admission model reads

A step inside a route's producer-to-evaluator span — the producer
itself, every intermediate step, and the evaluator — folds the route
target's artifact. A step no route spans folds false, because no
declared evaluation judges an artifact through it. The span is
graph-derived: a target reaches the route step through forward edges,
and the step reaches the route step through forward edges. The
historical bit therefore survives the corrective return to the
producer and every step between producer and evaluator: a step advance
is not a production event, a refine is not a production event, a
return is not a production event, and a parked gate is not a
production event.

The fold reads event history only, never the singular active contract,
so a duplicated projection folds the same answer the recovery that
owns it reads. The fold is total over any projection state: an
unreadable projection fails the fold with a typed refusal, and the
admission model carries the failure rather than assuming the bit is
false.

The artifact becomes stale on the latest of two events:

- a recorded verdict whose kind is not `ok`, or whose entry is
  incomparable with the approved result;
- a contract supersession that changed the objective the artifact must
  satisfy and that landed at or after the producer step.

The artifact clears on the latest production event at the declared
target that postdates the stale cause, where the production event is
the typed target production direct action (D4) or an accepted
`accept_worker_result` at the target step with the producing
dispatch and capability origin (D4). A re-recorded ok verdict, a
`dispatch_worker` completion, a fenced start, a verdict, an evidence
binding, a refinement, a return, a refine, an intermediate step
advance, or a parked gate never clears the bit, because none of them
is the typed production event. The ok verdict in a record_verdict
batch where at least one entry is `ok` is refused while the bit is
set; the same is true for a single ok entry. A `confirm_premise`
action while the bit is set refuses with the declared route as the
remedy. A correction request_correction that the gate admits moves
the work to the target step; only a fresh production there clears
the bit. The CD-0164 population and three-attempt wall key their
counting on the declared target, so the counting and the artifact
fold read the same target.

### D6. The work pin names the sequence and the supersede holds the step

The work pin reads the same table the admission model reads, through
the same function. The pin names the sequence the agent drives:

1. The unhealthy-verdict return at the evaluator step names one target
   step — the declared producer.
2. A `request_correction` action that the engine admits at the
   evaluator step moves the instance to the named target step with the
   bound operator approval, the diagnosis, the strategy, the affected
   predicate IDs, and the bound evidence references (CD-0143 D1).
3. The work pin names the next sequence as: fresh production at the
   target step; re-evaluation through the evaluator step; verdict or
   premise confirmation as the verdict or premise context permits.
4. The complete-step supersede is a different trigger. A
   `disproved_premise_at_complete` route at the pinned complete step
   admits `supersede_contract` (CD-0172 D1, CD-0186) and the engine
   moves the instance to the declared target with a successor contract
   bound. The complete-step supersede holds the step while the
   successor contract lands; an unhealthy verdict does not move the
   step under a `disproved_premise_at_complete` route, and a
   `disproved_premise_at_complete` route does not change the
   objective while the verdict remains unhealthy and fresh.

A supersede at a noncomplete step changes the objective and holds the
step. The unhealthy verdict, when it is retained and fresh, drives
`request_correction` exactly as the engine admits it, with the
exact operator approval that CD-0143 D1 requires. The fresh
production continuation is the next sequence the pin names. The work
pin never names a step the declared table does not cover; if the
declared table carries no route for the current step under the
current verdict, the pin offers the same-step owners the CD-0201 D3
and CD-0143 D2 routes already serve, and the gate does not delegate
the cross-step return.

CD-0172 D1, D2 and CD-0186 stay unchanged in scope: the complete-step
supersede is exact approval terminal and only implementation and
break_fix admit it, and this amendment does not broaden the
complete-step supersede. The convention cutoff — that the route
target is the producer D1 names and that no other work kind or
step refines the contract correction — stays in force.

### D7. Liveness, conformance, and the required public regressions

The liveness and conformance checks are exhaustive over every
registered definition version. A seeded false healthy witness must
fail the safety check, so a regression that drops the mirror
cannot pass silently. The conformance replay reads the
artifact-staleness bit the fold produces and replays the real
engine on witness paths. The conformance replay has no carveouts:
the seeded unmirrored fold produces a witness, and the mirror that
removes the witness is the test the conformance replay is.

The liveness check on the artifact-staleness bit is total. A
well-formed state whose `artifactStale` is true admits no
`record_verdict` action that records an `ok` verdict and admits no
`confirm_premise` action that steps the premise past the verdict
before the artifact is re-produced at the declared route target. A
seeded state that flips `artifactStale` to false produces exactly
the witnesses the check names, so a regression that drops the bit
fails with a witness, not a pass. The replay runs the real engine
on each witness path. The loader's artifact-staleness bit matches the
abstract successor at return, dispatch, acceptance, and evaluator re-entry.
Historical fence and hold state keep their existing semantics until a fresh start.

The liveness check is exhaustive across every registered
definition version. A new authored version that fails the
well-formed-exit or the reachable-state-reaches-terminal checks
fails registration or the liveness test, with a witness. A
released version whose quarantine routes fail the same checks fails
the liveness test on the quarantined data, with a witness.

Released pins receive routes on release without carryforward. The
release pass runs the liveness check on the quarantined routes for
each released (ref, version), and a release that fails the check
refuses to ship. The release pass never carries a forward route
from a prior version; the quarantine owns each version's routes,
and the version's history is preserved across the release pass.

The required public regressions are the D7 enumeration, frozen for
the `workflow.generic_one_off` version 13 fixture the CON-846
shape names, and the seven scenarios per route are:

```gherkin
Scenario: actual (verify) unhealthy_verdict request_correction to execute
  Given a workflow.generic_one_off v13 instance at verify
  When the agent asks the preflight for request_correction
  Then the preflight admits with target execute

Scenario: approved (verify) request_correction lands at execute
  Given a workflow.generic_one_off v13 instance at verify with a current unhealthy verdict
  When the agent records request_correction with the exact operator approval
  Then the instance moves to execute

Scenario: revision (verify) successor contract names the route
  Given a workflow.generic_one_off v13 instance at verify with an approved successor contract
  When the route reader resolves
  Then it returns the quarantined routes the (ref, version) holds

Scenario: unhealthy (verify) recorded bad verdict sets the bit
  Given a workflow.generic_one_off v13 instance at verify with a recorded bad verdict
  When the loader folds the state
  Then artifactStale is true

Scenario: exact correction (verify) request_correction with the bound evidence
  Given a workflow.generic_one_off v13 instance at verify with a current unhealthy verdict
  When the engine reads the request_correction
  Then the bound evidence and the exact operator approval are present

Scenario: fresh producer (verify) record_delivery at execute clears the bit
  Given a workflow.generic_one_off v13 instance at execute with a fresh record_delivery
  When the loader re-folds the state
  Then artifactStale is false

Scenario: independent verification (verify) ok verdict records after fresh production
  Given a workflow.generic_one_off v13 instance at verify with artifactStale false
  When the agent records record_verdict with verdict_kind ok
  Then the admission admits the verdict

Scenario: ok completion (verify) the instance reaches complete
  Given a workflow.generic_one_off v13 instance at verify with artifactStale false and an ok verdict
  When the agent records confirm_premise
  Then the admission admits confirm_premise and the instance moves to complete

Scenario: actual (complete) unhealthy_verdict request_correction to execute
  Given a workflow.generic_one_off v13 instance at complete
  When the agent asks the preflight for request_correction
  Then the preflight admits with target execute

Scenario: approved (complete) request_correction lands at execute
  Given a workflow.generic_one_off v13 instance at complete with a current unhealthy verdict
  When the agent records request_correction with the exact operator approval
  Then the instance moves to execute

Scenario: revision (complete) successor contract names the route
  Given a workflow.generic_one_off v13 instance at complete with an approved successor contract
  When the route reader resolves
  Then it returns the quarantined routes the (ref, version) holds

Scenario: unhealthy (complete) recorded bad verdict sets the bit
  Given a workflow.generic_one_off v13 instance at complete with a recorded bad verdict
  When the loader folds the state
  Then artifactStale is true

Scenario: exact correction (complete) request_correction with the bound evidence
  Given a workflow.generic_one_off v13 instance at complete with a current unhealthy verdict
  When the engine reads the request_correction
  Then the bound evidence and the exact operator approval are present

Scenario: fresh producer (complete) record_delivery at execute clears the bit
  Given a workflow.generic_one_off v13 instance at execute with a fresh record_delivery
  When the loader re-folds the state
  Then artifactStale is false

Scenario: independent verification (complete) ok verdict records after fresh production
  Given a workflow.generic_one_off v13 instance at complete with artifactStale false
  When the agent records record_verdict with verdict_kind ok
  Then the admission admits the verdict

Scenario: ok completion (complete) the instance receives the ok completion
  Given a workflow.generic_one_off v13 instance at complete with artifactStale false and an ok verdict
  When the agent records confirm_premise
  Then the admission admits confirm_premise and the instance completes
```

Public-tool journeys cover every authored route and both routes on
the released `workflow.generic_one_off` v13 pin named by CON-846.
The coverage guard enumerates authored definitions and refuses a
route without a journey. Released route goldens, frozen digest pins,
and exhaustive liveness cover every quarantined version.
`.concord/docs/knowledge/coverage/CD-0209.json` names the test evidence
for these distinct populations.

## Alternatives considered

- **A per-family step slice plus a work-kind whitelist.** Rejected:
  this is the per-shape pattern the CON-861 defect names. The
  work-kind whitelist is the rule CD-0201 D1 removes; per-family
  step slices are the same drift in a different shape. One table,
  one function, one reader, and one gate.
- **A graph walk from the evaluator step to the nearest effect
  step.** Rejected: the CD-0166 delivery gate already carries this
  walk and the user-issue defect it solves is a step-order drift on
  a different evaluator step. Two walks mean two owners; the gate
  inherits the table.
- **A release-side carryforward of prior version routes.** Rejected:
  the quarantine and the table are the only sources, and the
  history of a released version stays the history of that version
  (CD-0115 D1). A carryforward would let a quarantined shape drift
  from the graph the next release reads.
- **A `dispatch_worker` completion alone as production.** Rejected:
  a dispatch is intent, not production. The acceptance at the
  target step is the production event, and the dispatch must
  precede the acceptance in the same event history.
- **A fence-action production rule on `record_verdict` or
  `record_delivery` only.** Rejected: `record_finding` and
  `record_decision` are also typed production direct actions, and
  the research and architecture-spike producers own the artifact
  through them. The closed positive production classification is
  total, and a family that gains a new producing mechanism
  declares it here.
- **Broadening the complete-step supersede to research,
  architecture_spike, ops_runbook, static_analysis, or
  generic_one_off.** Rejected: CD-0172 D1 and CD-0186 are in force
  and the convention cutoff stays exact. The complete-step
  supersede is the implementation or break_fix external-effect
  return, and the rest of the families refuse the route.
- **Rewriting CD-0143, CD-0201, CD-0204 in place.** Rejected:
  accepted law changes through an explicit amendment record that
  carries its own authority and approval. The amendments D1 names
  are pointer amendments only, and the body of each accepted
  record is preserved.
- **Fabricating a workflow record for the operator authorization.**
  Rejected: CD-0122 D2 forbids fabricating, rewriting, or
  inferring Concord workflow records. The amendment runs under the
  outside-repair authorization, and the work-id metadata the
  parent updates is the latest managed proposal for CON-861, not a
  record invented for this amendment.

## Consequences

The runtime cross-step correction admits one declared target per
(step, trigger) pair. The gate refuses the same shape that CON-846
admits: a definition whose steps record a verdict, confirm a
premise, or host the late-verdict context, and that resolves no
declared unhealthy-verdict route for the step. The folded state the
admission model carries is the same slice the runtime reads, so
the recorded move and the admitted intent cannot disagree about
the return. The artifact-staleness bit is folded once over event
history and read by the admission model and the conformance
replay identically; a regression that loses the mirror cannot
pass silently because the seeded unmirrored fold produces a
witness.

A start alone is never production. A `dispatch_worker` completion
alone is never production. A verdict, a refinement review
acceptance, a candidate revision, an impact declaration, a
successor link, a checkpoint, or an evidence binding is never
production. The closed positive production classification is the
only production rule, and a family that gains a new producing
mechanism declares it here.

CD-0164 population and the three-attempt wall key their counting
on the declared target, so the counting and the artifact fold
read the same target. CD-0143 D2, D3, D4 stay in force:
`request_correction` returns the instance to the declared
producer step, the bound on correction sequences holds,
and the fail-closed completion keeps the latest approved
predicate unhealthy or incomparable with the approved result
refusing. CD-0115 D1 stays in force: the canonical bytes of every
released version are unchanged, and the quarantined data own
every released version's routes.

The release-on-route pass for the 108 released versions ships
without carryforward. The history of a released version is
preserved across the pass. A new authored version's routes ship
with the version; a released version's routes ship with the
quarantine; a regression that strands a released pin fails the
liveness test with a witness.

The provenance of operator approval, the CD-0164 population, and
the wall stay keyed to the declared target. The covered evidence
references for a `request_correction` are the bound evidence
references the engine reads; a correction that names evidence the
engine cannot bind refuses at the gate.

The coverage shard names the registration, quarantine, production,
admission, liveness, conformance, and public-tool tests. Repository
verification supplies this evidence, not a fabricated workflow record.

## Verification

- `internal/store.TestReleasedDigestsIgnoreRecoveryRouteField` proves
  every released version's canonical bytes and digest stay unchanged
  when the field is absent or empty.
- `internal/store.TestWorkflowRecoveryRoutesGoldenPerReleasedVersion`
  proves the quarantined routes are golden per (ref, version), and
  the per-version graph invariants hold against the quarantined data.
- `internal/store.TestCompleteStepCorrectionRoutesCoverOnlyTheSupportedFamilies`
  proves the CD-0172 D1 and CD-0186 boundary: only implementation
  and break_fix resolve a `disproved_premise_at_complete` route, the
  route sits on the complete action step of that version, and the
  target is the implementation `execution` or break_fix `repair`
  step.
- `internal/store.TestRecoveryRouteRegistrationRefusals` proves the
  D2 structural gate: missing route, ambiguous (step, trigger),
  unknown trigger, unknown step, unknown action, unknown target,
  unhealthy trigger paired with `supersede_contract`, complete
  trigger paired with `request_correction`, complete trigger off
  the complete step, unhealthy trigger on a non-evaluator step,
  target equal to the route step, target terminal, target with
  `approve_contract`, target not upstream, and a second failure
  edge all refuse at registration with a typed failure.
- `internal/store.TestUnreleasedEvaluatorDefinitionsWithoutATableRefuse`
  proves the CON-846 gate: a definition whose steps record a
  verdict, confirm a premise, or host the late-verdict context
  refuses registration when the quarantine does not name its (ref,
  version) and the declared table is nil or empty.
- `internal/store.TestAuthoredRecoveryRoutesOverrideTheQuarantine`
  proves the D3 reader: a declared table wins over any quarantined
  data for the same (ref, version), the returned slice is a
  defensive copy, and the reader does not mutate the definition it
  read.
- `internal/store.TestWorkflowRecoveryRoutesDoNotMutateOldCanonicalDefinitions`
  proves the canonical-bytes read is a pure read: a released
  definition and a declared definition leave their canonical bytes
  unchanged across a route read.
- `internal/store.TestNewAuthoredVersionsDeclareRouteTables` proves
  every authored version declares its D3 table, registers under
  `ValidateWorkflowDefinition`, and covers the evaluator steps the
  family's graph declares.
- `internal/store.TestReleasedVersionsRegisterWithoutDeclaredTables`
  proves the compatibility boundary: a released definition with a
  nil table still registers, and its routes resolve through the
  quarantine.
- `internal/store.TestRecoveryRouteTargetMustProduceArtifact` proves
  the D4 producer rule: a target that carries no `dispatch_worker`,
  no fenced-start action, and no direct `record_finding`,
  `record_decision`, or `record_delivery` action refuses at
  registration.
- `internal/store.TestWellFormedAdmissionStateHasNonContinuityExit`
  and `internal/store.TestReachableAdmissionStateReachesTerminal`
  prove the CD-0201 D2 liveness law over every registered definition
  version.
- `internal/store.TestStaleArtifactStateAdmitsNoBadToOkWithoutReproduction`
  and `internal/store.TestStaleArtifactCheckNamesSeededWitness` prove
  the D7 exhaustive liveness and the seeded false-healthy witness:
  the conformance replay names exactly the witnesses the mirror
  removes, and the seeded unmirrored fold produces a witness.
- `internal/store.TestProductionBindsToDispatchOriginAndCapability`
  proves the D4 worker production frontier: an accepted worker
  result at the declared target clears the bit only when the dispatch and
  capability events for the same attempt precede the acceptance in
  the same event history and all three postdate the stale cause,
  and the capability class is `implementation`, `design`, or
  `research`.
- `internal/store.TestResearchAndSpikeProducersClearStaleness` proves
  direct production without a worker acceptance.
- `internal/store.TestUnfencedCheckpointEpochUsesCausalPrefix` proves
  repeated direct checkpoints keep distinct identities and replay
  derives each epoch from the preceding event prefix.
- `internal/agent.TestWorkflowRecoveryRoutesDeclareEveryEvaluatorStep`
  enumerates authored routes and refuses missing public journeys.
- `internal/agent.TestWorkflowRecoveryRoutesJourneyAdmitsEveryDeclaredRoute`
  proves approved revision, unhealthy evaluation, exact correction
  approval, fresh production, independent evaluation, and completion.
- `internal/agent.TestWorkflowRecoveryRoutesCompleteStepSupersedeAdmitsEveryDeclaredRoute`
  proves both supported complete-step supersessions through fresh
  production and completion without moving the step through fixture SQL.
- `python3 scripts/check-doc-contract.py` proves this record
  carries the current decision outline and passes the writing
  rules.
- `python3 scripts/check-knowledge-index.py` and
  `python3 scripts/check-knowledge-closure.py` prove this record
  registers with a current content hash and no unprocessed document
  remains.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the
  CD-0209 identifier allocates once.
